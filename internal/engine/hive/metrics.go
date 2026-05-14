package hive

import (
	"sync/atomic"
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

func (m *metrics) loadTotalLatency() int64 {
	var n int64
	for i := range m.totalLatency {
		n += atomic.LoadInt64(&m.totalLatency[i].value)
	}
	return n
}
