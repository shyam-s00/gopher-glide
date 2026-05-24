package hive

import (
	"math"
	"sort"
	"sync/atomic"
)

// ── latency buffer capacity bounds ───────────────────────────────────────────

const (
	// minLatencyBufCap is the smallest ring buffer we ever allocate.
	// 1 024 slots is enough for any test that runs at ≤ 1 RPS for ~17 min.
	minLatencyBufCap = 1_024

	// maxLatencyBufCap caps memory regardless of run duration or RPS.
	// 1 000 000 × 8 B (atomic.Uint64) = 8 MB per engine instance.
	//
	// At 50 000 RPS the ring wraps roughly every 20 s, so the snapshot
	// always reflects recent behaviour.  1 M samples is far more than
	// needed for statistically sound p50 / p95 / p99 percentile estimates.
	maxLatencyBufCap = 1_000_000

	// latencySampleSize is the maximum number of ring-buffer slots read and
	// sorted on each call to computeLatency().
	//
	// Without this cap, computeLatency() is O(n log n) + O(n) allocation per
	// TUI refresh, where n scales up to maxLatencyBufCap (1M entries = 8MB
	// allocation + ~20M comparisons every 100ms).
	//
	// With the cap, each snapshot:
	//   • allocates a fixed ~32 KB (4096 × 8 B float64)
	//   • sorts in O(4096 × log₂ 4096) ≈ 49 K comparisons  (<0.1 ms)
	//   • gives statistically accurate p99 estimates (< 1% error for
	//     realistic latency distributions at any sample count)
	//
	// We always sample the most recently written entries so the percentiles
	// reflect current behaviour, not a mix of old and new data.
	latencySampleSize = 4_096
)

// latencyBuf is a pre-allocated lock-free ring buffer for recording per-request
// latencies as float64 millisecond values.
//
// Writers atomically claim a slot via n.Add(1) and store the value as its
// IEEE-754 bit representation in the corresponding atomic.Uint64 cell.
// Reads snapshot the count once, then load each cell independently — no lock
// is ever held. Wrapping at capacity bounds memory use regardless of run length.
type latencyBuf struct {
	buf []atomic.Uint64 // ring of float64 values stored as Float64bits
	n   atomic.Int64    // total writes; pos = (n-1) % len(buf)
}

// newLatencyBuf allocates a latencyBuf with the given capacity.
// A minimum of 1 slot is always allocated.
func newLatencyBuf(cap int) latencyBuf {
	if cap < 1 {
		cap = 1
	}
	return latencyBuf{buf: make([]atomic.Uint64, cap)}
}

// computeLatency returns min, max, p50, p95, and p99 latencies in milliseconds.
//
// Cost per call: O(latencySampleSize × log latencySampleSize) time,
// O(latencySampleSize) allocation — both are constant regardless of how long
// the run has been active or how many requests have been recorded.
//
// We sample the latencySampleSize most recently written slots from the ring
// buffer. Reading the most recent entries (rather than a spread sample) ensures
// the percentiles track current behaviour, which is especially important during
// ramp stages where response times change rapidly.
//
// Concurrent-safety: count is loaded once; individual slot loads may see a
// write that happened after the count snapshot. The resulting off-by-one is
// bounded to one entry and is acceptable for a display-only metric.
func (e *Engine) computeLatency() (min, max, p50, p95, p99 float64) {
	lb := e.latBuf.Load()
	if lb == nil {
		return
	}
	count := lb.n.Load()
	capSize := int64(len(lb.buf))
	if count == 0 || capSize == 0 {
		return
	}

	// valid = number of occupied slots (ring wraps once count > capSize).
	valid := count
	if valid > capSize {
		valid = capSize
	}

	// n = entries we will actually read and sort — capped at latencySampleSize.
	n := valid
	if n > latencySampleSize {
		n = latencySampleSize
	}

	// Read the n most recently written slots. The last write landed at
	// slot (count-1) % capSize; walking backwards gives the n newest.
	data := make([]float64, n)
	for i := int64(0); i < n; i++ {
		pos := (count - 1 - i) % capSize
		data[i] = math.Float64frombits(lb.buf[pos].Load())
	}

	sort.Float64s(data)
	min = data[0]
	max = data[n-1]
	p50 = percentile(data, 50)
	p95 = percentile(data, 95)
	p99 = percentile(data, 99)
	return
}

// percentile returns the p-th percentile value from a sorted slice using
// linear interpolation between adjacent ranks.
//
// p must be in [0, 100]. data must be sorted in ascending order.
// Returns 0 for an empty slice.
func percentile(data []float64, p float64) float64 {
	if len(data) == 0 {
		return 0
	}
	idx := (p / 100) * float64(len(data)-1)
	lower := int(idx)
	upper := lower + 1
	if upper >= len(data) {
		return data[lower]
	}
	frac := idx - float64(lower)
	return data[lower] + frac*(data[upper]-data[lower])
}
