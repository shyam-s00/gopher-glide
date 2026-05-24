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
// It reads n once to determine how many entries are available, then loads
// each slot independently — no lock is held at any point. A concurrent
// writer may update a slot between the n read and the slot load; the resulting
// off-by-one is bounded to one entry and is acceptable for a display-only
// metric sampled at ~10 Hz.
func (e *Engine) computeLatency() (min, max, p50, p95, p99 float64) {
	lb := e.latBuf.Load()
	if lb == nil {
		return
	}
	count := lb.n.Load()
	cap := int64(len(lb.buf))
	if count == 0 || cap == 0 {
		return
	}

	// Number of valid entries is min(count, cap) — older entries were
	// overwritten once the ring wrapped.
	n := count
	if n > cap {
		n = cap
	}

	data := make([]float64, n)
	for i := int64(0); i < n; i++ {
		data[i] = math.Float64frombits(lb.buf[i].Load())
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
