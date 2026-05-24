package hive

import (
	"sync"
	"testing"
	"time"
	"unsafe"
)

// ── paddedCounter ─────────────────────────────────────────────────────────────

func TestPaddedCounter_CacheLineSize(t *testing.T) {
	// Each paddedCounter must be exactly 64 bytes so it occupies one CPU cache
	// line and no two shards share a line.
	got := unsafe.Sizeof(paddedCounter{})
	if got != 64 {
		t.Errorf("paddedCounter size: want 64 bytes (one cache line), got %d", got)
	}
}

func TestPaddedCounter_ZeroValue(t *testing.T) {
	var c paddedCounter
	if c.value != 0 {
		t.Errorf("zero paddedCounter should have value 0, got %d", c.value)
	}
}

// ── metrics — write path ──────────────────────────────────────────────────────

func TestMetrics_ZeroState(t *testing.T) {
	var m metrics
	if v := m.loadTotalRequests(); v != 0 {
		t.Errorf("totalRequests: want 0, got %d", v)
	}
	if v := m.loadSuccessCount(); v != 0 {
		t.Errorf("successCount: want 0, got %d", v)
	}
	if v := m.loadFailureCount(); v != 0 {
		t.Errorf("failureCount: want 0, got %d", v)
	}
	if v := m.loadTotalLatency(); v != 0 {
		t.Errorf("totalLatency: want 0, got %d", v)
	}
}

func TestMetrics_IncAndLoad(t *testing.T) {
	var m metrics
	// Use shard 0 for simplicity.
	m.incTotalRequests(0)
	m.incTotalRequests(0)
	m.incSuccess(0)
	m.incFailure(0)
	m.addLatency(0, 250)

	if v := m.loadTotalRequests(); v != 2 {
		t.Errorf("totalRequests: want 2, got %d", v)
	}
	if v := m.loadSuccessCount(); v != 1 {
		t.Errorf("successCount: want 1, got %d", v)
	}
	if v := m.loadFailureCount(); v != 1 {
		t.Errorf("failureCount: want 1, got %d", v)
	}
	if v := m.loadTotalLatency(); v != 250 {
		t.Errorf("totalLatency: want 250, got %d", v)
	}
}

// ── metrics — shard distribution ─────────────────────────────────────────────

func TestMetrics_ShardDistribution(t *testing.T) {
	var m metrics
	// Write 1 request to every shard individually.
	for i := 0; i < numShards; i++ {
		m.incTotalRequests(i)
	}
	// load*() must sum all shards — result must equal numShards.
	if got := m.loadTotalRequests(); got != numShards {
		t.Errorf("loadTotalRequests after writing 1 per shard: want %d, got %d", numShards, got)
	}
}

func TestMetrics_ShardModulo(t *testing.T) {
	var m metrics
	// shard index wraps correctly: shard == numShards must map to shard 0.
	m.incTotalRequests(0)
	m.incTotalRequests(numShards) // same physical shard as 0
	if got := m.loadTotalRequests(); got != 2 {
		t.Errorf("shard modulo: want 2 (shards 0 and numShards alias), got %d", got)
	}
}

func TestMetrics_IndependentShards(t *testing.T) {
	var m metrics
	// Write different amounts to different shards.
	m.incTotalRequests(0)
	m.incTotalRequests(0)
	m.incTotalRequests(1)  // shard 1 gets 1
	m.incTotalRequests(15) // shard 15 gets 1

	if got := m.loadTotalRequests(); got != 4 {
		t.Errorf("independent shards: want 4, got %d", got)
	}
}

func TestMetrics_LatencyShardAccumulation(t *testing.T) {
	var m metrics
	// Spread latency across every shard, 10ms each.
	for i := 0; i < numShards; i++ {
		m.addLatency(i, 10)
	}
	want := int64(numShards * 10)
	if got := m.loadTotalLatency(); got != want {
		t.Errorf("loadTotalLatency: want %d, got %d", want, got)
	}
}

// ── metrics — counter invariant ───────────────────────────────────────────────

func TestMetrics_CounterInvariant(t *testing.T) {
	var m metrics
	// Distribute 20 requests across shards; alternate success/failure.
	for i := 0; i < 20; i++ {
		shard := i % numShards
		m.incTotalRequests(shard)
		if i%3 == 0 {
			m.incFailure(shard)
		} else {
			m.incSuccess(shard)
		}
	}
	total := m.loadTotalRequests()
	success := m.loadSuccessCount()
	failure := m.loadFailureCount()
	if total != success+failure {
		t.Errorf("counter invariant: total=%d success=%d failure=%d (success+failure=%d)",
			total, success, failure, success+failure)
	}
}

// ── rpsWindow ────────────────────────────────────────────────────────────────

func TestRpsWindow_ZeroBeforeRecord(t *testing.T) {
	var w rpsWindow
	if r := w.rate(); r != 0 {
		t.Errorf("want 0 before any records, got %f", r)
	}
}

func TestRpsWindow_RecordAndRate(t *testing.T) {
	var w rpsWindow
	prev := time.Now().Unix() - 1
	slot := int(prev % rpsWindowSize)
	w.seconds[slot].Store(prev)
	w.buckets[slot].Store(50)

	if r := w.rate(); r <= 0 {
		t.Errorf("want rate > 0 after recording, got %f", r)
	}
}

func TestRpsWindow_ClearsStaleSlot(t *testing.T) {
	var w rpsWindow
	old := time.Now().Unix() - 100
	slot := int(old % rpsWindowSize)
	w.seconds[slot].Store(old)
	w.buckets[slot].Store(999)

	if r := w.rate(); r != 0 {
		t.Errorf("stale bucket must not affect rate, got %f", r)
	}
}

func TestRpsWindow_RecordResetsStaleSlot(t *testing.T) {
	var w rpsWindow
	now := time.Now().Unix()
	slot := int(now % rpsWindowSize)
	w.seconds[slot].Store(now - 1000)
	w.buckets[slot].Store(999)

	w.record(1)

	if got := w.seconds[slot].Load(); got != now {
		t.Errorf("Record() should have reset Seconds[%d] to %d, got %d", slot, now, got)
	}
	if got := w.buckets[slot].Load(); got != 1 {
		t.Errorf("Record() should have reset Buckets[%d] to 1, got %d", slot, got)
	}
}

func TestRpsWindow_AccumulatesWithinSameSecond(t *testing.T) {
	var w rpsWindow
	w.record(10)
	w.record(5)
	w.record(3)

	now := time.Now().Unix()
	slot := int(now % rpsWindowSize)

	if got := w.buckets[slot].Load(); got != 18 {
		t.Errorf("multiple Record() calls in same second: want 18, got %d", got)
	}
}

func TestRpsWindow_RateAveragesOverWindow(t *testing.T) {
	var w rpsWindow
	now := time.Now().Unix()
	for age := int64(1); age < rpsWindowSize; age++ {
		sec := now - age
		s := int(sec % rpsWindowSize)
		w.seconds[s].Store(sec)
		w.buckets[s].Store(100)
	}
	if rate := w.rate(); rate != 100 {
		t.Errorf("rate: want 100.0, got %f", rate)
	}
}

// ── rpsWindow: concurrent access under -race ──────────────────────────────────

// TestRpsWindow_Concurrent_Record verifies that many goroutines calling
// record() simultaneously produce no data races. The exact final count in
// any slot is not asserted — the window's second-boundary reset intentionally
// allows a bounded clobber — but no slot must go negative and the call must
// not panic.
func TestRpsWindow_Concurrent_Record(t *testing.T) {
	t.Parallel()
	var w rpsWindow
	const goroutines = 100
	const opsEach = 500

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < opsEach; j++ {
				w.record(1)
			}
		}()
	}
	wg.Wait()

	// Buckets must be non-negative — no goroutine may have stored a negative value.
	for i := range w.buckets {
		if v := w.buckets[i].Load(); v < 0 {
			t.Errorf("buckets[%d] went negative: %d", i, v)
		}
	}
}

// TestRpsWindow_Concurrent_RecordAndRate verifies that concurrent writers
// (record) and readers (rate) produce no data races and that rate() never
// returns a negative value.
func TestRpsWindow_Concurrent_RecordAndRate(t *testing.T) {
	t.Parallel()
	var w rpsWindow
	const goroutines = 50
	const opsEach = 300

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < opsEach; j++ {
				w.record(1)
			}
		}()
	}
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < opsEach; j++ {
				if r := w.rate(); r < 0 {
					t.Errorf("rate() returned negative value: %f", r)
				}
			}
		}()
	}
	wg.Wait()
}

// TestRpsWindow_Concurrent_ResetRaceWithRecord verifies that reset() and
// record() can run concurrently without a data race. After all goroutines
// finish, every slot must have a non-negative bucket value.
func TestRpsWindow_Concurrent_ResetRaceWithRecord(t *testing.T) {
	t.Parallel()
	var w rpsWindow
	const goroutines = 40
	const opsEach = 200

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		even := i%2 == 0
		go func() {
			defer wg.Done()
			for j := 0; j < opsEach; j++ {
				if even {
					w.record(1)
				} else {
					w.reset()
				}
			}
		}()
	}
	wg.Wait()

	for i := range w.buckets {
		if v := w.buckets[i].Load(); v < 0 {
			t.Errorf("buckets[%d] negative after concurrent reset+record: %d", i, v)
		}
	}
}
