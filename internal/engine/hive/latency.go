package hive

import "sort"

// computeLatency returns min, max, p50, p95, and p99 latencies in milliseconds.
//
// It takes a read-lock only long enough to copy the slice, then releases it
// before sorting so the hot write path (Actor goroutines appending) is never
// blocked by an in-progress sort.
//
// All values are zero when no requests have completed yet.
func (e *Engine) computeLatency() (min, max, p50, p95, p99 float64) {
	e.latencyMu.RLock()
	if len(e.latencies) == 0 {
		e.latencyMu.RUnlock()
		return
	}
	data := make([]float64, len(e.latencies))
	copy(data, e.latencies)
	e.latencyMu.RUnlock()

	sort.Float64s(data)
	n := len(data)
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
