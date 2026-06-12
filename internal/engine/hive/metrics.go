package hive

import (
	"sync/atomic"
	"time"
)

// ── Striped atomics ───────────────────────────────────────────────────────────

// numShards is the number of independent counter shards.
// 16 shards eliminates contention across machines with up to 32 cores.
// Must be a power of two so shard % numShards compiles to a bitwise AND.
const numShards = 16

// paddedCounter is a cache-line padded int64 counter.
//
// Layout: 8-byte value + 56-byte padding = 64 bytes = one CPU cache line.
// Padding ensures that each shard lives on its own cache line so concurrent
// writes from goroutines on different cores never invalidate each other.
// Without padding, all 16 values would share 1–2 cache lines and any write
// would still trigger cross-core invalidation — negating the striping benefit.
type paddedCounter struct {
	value int64
	_     [56]byte
}

// metrics holds all sharded request counters.
//
// Writers (Actor goroutines) call inc*(shard) where shard = actorIndex % numShards.
// Readers (GetMetrics) call load*() which sums all 16 shards.
//
// Trade-off: writes are O(1) and contention-free; reads are O(numShards) and
// slightly slower — acceptable at the TUI's ~10 Hz refresh rate.
//
// Note: reading across the four separate shard arrays is not collectively
// atomic. A snapshot taken during a live run may transiently show
// success+failure off by 1 vs. totalRequests. This is acceptable for a
// display metric and correct over any observation window > a few microseconds.
type metrics struct {
	totalRequests [numShards]paddedCounter
	successCount  [numShards]paddedCounter
	failureCount  [numShards]paddedCounter
	droppedCount  [numShards]paddedCounter
	totalLatency  [numShards]paddedCounter // cumulative milliseconds
}

// ── Write path (hot) ──────────────────────────────────────────────────────────

func (m *metrics) incTotalRequests(shard int) {
	atomic.AddInt64(&m.totalRequests[shard%numShards].value, 1)
}

func (m *metrics) incSuccess(shard int) {
	atomic.AddInt64(&m.successCount[shard%numShards].value, 1)
}

func (m *metrics) incFailure(shard int) {
	atomic.AddInt64(&m.failureCount[shard%numShards].value, 1)
}

func (m *metrics) incDropped(shard int) {
	atomic.AddInt64(&m.droppedCount[shard%numShards].value, 1)
}

func (m *metrics) addLatency(shard int, ms int64) {
	atomic.AddInt64(&m.totalLatency[shard%numShards].value, ms)
}

// ── Read path (cold) ──────────────────────────────────────────────────────────

func (m *metrics) loadTotalRequests() int64 {
	var n int64
	for i := range m.totalRequests {
		n += atomic.LoadInt64(&m.totalRequests[i].value)
	}
	return n
}

func (m *metrics) loadSuccessCount() int64 {
	var n int64
	for i := range m.successCount {
		n += atomic.LoadInt64(&m.successCount[i].value)
	}
	return n
}

func (m *metrics) loadFailureCount() int64 {
	var n int64
	for i := range m.failureCount {
		n += atomic.LoadInt64(&m.failureCount[i].value)
	}
	return n
}

func (m *metrics) loadDroppedCount() int64 {
	var n int64
	for i := range m.droppedCount {
		n += atomic.LoadInt64(&m.droppedCount[i].value)
	}
	return n
}

func (m *metrics) loadTotalLatency() int64 {
	var n int64
	for i := range m.totalLatency {
		n += atomic.LoadInt64(&m.totalLatency[i].value)
	}
	return n
}

// ── rpsWindow ────────────────────────────────────────────────────────────────

// rpsWindowSize is the number of past seconds averaged to compute current RPS.
// 3 keeps the reading responsive to bursts without single-second oscillation.
const rpsWindowSize = 3

// rpsWindow is a fixed-size ring of per-second request counts used to compute
// a smooth, responsive current-RPS without cumulative lag.
//
// Each slot in the ring owns one calendar second (unix timestamp).
// When record() is called in a new second it resets the stale slot first,
// so old data never bleeds forward.
//
// All fields use atomic.Int64 — no mutex is held on the hot request path.
type rpsWindow struct {
	buckets [rpsWindowSize]atomic.Int64 // request count recorded in that second
	seconds [rpsWindowSize]atomic.Int64 // unix second the corresponding bucket belongs to
}

// reset zeroes all slots atomically.
//
// Callers may observe a transient zero across slots if rate() is called
// concurrently — this is acceptable for a display-only metric.
func (w *rpsWindow) reset() {
	for i := range w.buckets {
		w.seconds[i].Store(0)
		w.buckets[i].Store(0)
	}
}

// record increments the count for the current second.
//
// The slot for the current unix second is identified via modulo. If the slot's
// stored timestamp differs from now it belongs to a past second, so the bucket
// is reset before incrementing. Swap is used atomically: the goroutine that
// wins the Swap transition (old ≠ now) performs the Store(0). Any goroutine
// that arrives after the transition sees old == now and skips the reset.
//
// At a second boundary there is a narrow window where a late-arriving goroutine
// may Store(0) after an earlier goroutine already incremented the fresh bucket,
// clobbering that count. This is accepted — the metric is display-only and
// self-corrects within one second.
func (w *rpsWindow) record(count int64) {
	now := time.Now().Unix()
	slot := int(now % rpsWindowSize)
	if w.seconds[slot].Swap(now) != now {
		w.buckets[slot].Store(0)
	}
	w.buckets[slot].Add(count)
}

// rate returns the average request rate over the past (rpsWindowSize-1)
// fully-completed seconds. The current (still-accumulating) second is
// excluded so the reading never oscillates at second boundaries.
//
// Each slot is read with two independent atomic loads (seconds then buckets).
// A concurrent record() call may land between the two loads, causing the
// bucket to be read before the second is updated, or vice-versa. The
// resulting off-by-one is bounded to a single request and is acceptable for
// a display-only metric sampled at ~10 Hz.
func (w *rpsWindow) rate() float64 {
	now := time.Now().Unix()
	var total int64
	for i := 0; i < rpsWindowSize; i++ {
		age := now - w.seconds[i].Load()
		if age >= 1 && age < rpsWindowSize {
			total += w.buckets[i].Load()
		}
	}
	windowSecs := float64(rpsWindowSize - 1)
	if windowSecs < 1 {
		windowSecs = 1
	}
	return float64(total) / windowSecs
}
