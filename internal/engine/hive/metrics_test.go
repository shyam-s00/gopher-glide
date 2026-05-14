package hive

import (
	"testing"
	"time"
	"unsafe"

	"github.com/shyam-s00/gopher-glide/internal/engine"
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
	var w engine.RpsWindow
	if r := w.Rate(); r != 0 {
		t.Errorf("want 0 before any records, got %f", r)
	}
}

func TestRpsWindow_RecordAndRate(t *testing.T) {
	var w engine.RpsWindow
	prev := time.Now().Unix() - 1
	slot := int(prev % engine.RpsWindowSize)
	w.Seconds[slot] = prev
	w.Buckets[slot] = 50

	if r := w.Rate(); r <= 0 {
		t.Errorf("want rate > 0 after recording, got %f", r)
	}
}

func TestRpsWindow_ClearsStaleSlot(t *testing.T) {
	var w engine.RpsWindow
	old := time.Now().Unix() - 100
	slot := int(old % engine.RpsWindowSize)
	w.Seconds[slot] = old
	w.Buckets[slot] = 999

	if r := w.Rate(); r != 0 {
		t.Errorf("stale bucket must not affect rate, got %f", r)
	}
}

func TestRpsWindow_RecordResetsStaleSlot(t *testing.T) {
	var w engine.RpsWindow
	now := time.Now().Unix()
	slot := int(now % engine.RpsWindowSize)
	w.Seconds[slot] = now - 1000
	w.Buckets[slot] = 999

	w.Record(1)

	w.Mu.Lock()
	if w.Seconds[slot] != now {
		t.Errorf("Record() should have reset Seconds[%d] to %d, got %d", slot, now, w.Seconds[slot])
	}
	if w.Buckets[slot] != 1 {
		t.Errorf("Record() should have reset Buckets[%d] to 1, got %d", slot, w.Buckets[slot])
	}
	w.Mu.Unlock()
}

func TestRpsWindow_AccumulatesWithinSameSecond(t *testing.T) {
	var w engine.RpsWindow
	w.Record(10)
	w.Record(5)
	w.Record(3)

	now := time.Now().Unix()
	slot := int(now % engine.RpsWindowSize)

	w.Mu.Lock()
	got := w.Buckets[slot]
	w.Mu.Unlock()

	if got != 18 {
		t.Errorf("multiple Record() calls in same second: want 18, got %d", got)
	}
}

func TestRpsWindow_RateAveragesOverWindow(t *testing.T) {
	var w engine.RpsWindow
	now := time.Now().Unix()
	for age := int64(1); age < engine.RpsWindowSize; age++ {
		sec := now - age
		s := int(sec % engine.RpsWindowSize)
		w.Seconds[s] = sec
		w.Buckets[s] = 100
	}
	if rate := w.Rate(); rate != 100 {
		t.Errorf("rate: want 100.0, got %f", rate)
	}
}
