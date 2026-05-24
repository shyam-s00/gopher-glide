package hive

import (
	"math"
	"testing"
)

// seedLatBuf writes values directly into e.latBuf, simulating what
// recordLatency() will do once the actor write path is live (1.4.1h).
// Using the same atomic ring-buffer protocol: claim a slot via n.Add(1),
// then store Float64bits at pos = (idx % cap).
func seedLatBuf(e *Engine, values ...float64) {
	lb := e.latBuf.Load()
	for _, v := range values {
		idx := lb.n.Add(1) - 1
		pos := idx % int64(len(lb.buf))
		lb.buf[pos].Store(math.Float64bits(v))
	}
}

// ── percentile ────────────────────────────────────────────────────────────────

func TestPercentile_Empty(t *testing.T) {
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("want 0 for empty slice, got %f", got)
	}
}

func TestPercentile_SingleElement(t *testing.T) {
	if got := percentile([]float64{42}, 99); got != 42 {
		t.Errorf("want 42 for single-element slice at p99, got %f", got)
	}
}

func TestPercentile_SingleElement_AllPercentiles(t *testing.T) {
	for _, p := range []float64{0, 25, 50, 75, 90, 95, 99, 100} {
		if got := percentile([]float64{7}, p); got != 7 {
			t.Errorf("percentile(p=%.0f) on single element: want 7, got %f", p, got)
		}
	}
}

func TestPercentile_Table(t *testing.T) {
	data := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		p    float64
		want float64
		tol  float64
	}{
		{p: 0, want: 1, tol: 0.01},    // minimum
		{p: 50, want: 5.5, tol: 0.2},  // midpoint interpolation
		{p: 90, want: 9.1, tol: 0.2},  // near-max interpolation
		{p: 100, want: 10, tol: 0.01}, // maximum
	}
	for _, c := range cases {
		got := percentile(data, c.p)
		if math.Abs(got-c.want) > c.tol {
			t.Errorf("percentile(%v, %.0f): want %.3f ±%.3f, got %.3f",
				data, c.p, c.want, c.tol, got)
		}
	}
}

func TestPercentile_Interpolation(t *testing.T) {
	// Exact values for a 5-element sorted slice: [10, 20, 30, 40, 50]
	// idx = (p/100) * (n-1); lower = floor(idx); upper = lower+1
	data := []float64{10, 20, 30, 40, 50}
	cases := []struct {
		p    float64
		want float64
	}{
		{0, 10},   // idx=0.0  → data[0]=10
		{25, 20},  // idx=1.0  → data[1]=20
		{50, 30},  // idx=2.0  → data[2]=30
		{75, 40},  // idx=3.0  → data[3]=40
		{100, 50}, // idx=4.0  → data[4]=50
		// Between steps: p=12.5 → idx=0.5 → 10 + 0.5*(20-10) = 15
		{12.5, 15},
	}
	for _, c := range cases {
		got := percentile(data, c.p)
		if math.Abs(got-c.want) > 0.001 {
			t.Errorf("percentile(p=%.2f): want %.3f, got %.3f", c.p, c.want, got)
		}
	}
}

// ── computeLatency ────────────────────────────────────────────────────────────

func TestComputeLatency_Empty(t *testing.T) {
	e := New()
	minL, maxL, p50, p95, p99 := e.computeLatency()
	if minL != 0 || maxL != 0 || p50 != 0 || p95 != 0 || p99 != 0 {
		t.Errorf("all latency values should be 0 when no data: min=%f max=%f p50=%f p95=%f p99=%f",
			minL, maxL, p50, p95, p99)
	}
}

func TestComputeLatency_SingleElement(t *testing.T) {
	e := New()
	seedLatBuf(e, 42.0)

	minL, maxL, p50, p95, p99 := e.computeLatency()
	for name, got := range map[string]float64{"min": minL, "max": maxL, "p50": p50, "p95": p95, "p99": p99} {
		if math.Abs(got-42.0) > 0.001 {
			t.Errorf("%s: want 42.0, got %f", name, got)
		}
	}
}

func TestComputeLatency_Values(t *testing.T) {
	e := New()
	// Seed a known sorted set: 10, 20, 30, 40, 50 ms
	seedLatBuf(e, 30, 10, 50, 20, 40)

	minL, maxL, p50, p95, p99 := e.computeLatency()

	if math.Abs(minL-10) > 0.001 {
		t.Errorf("min: want 10, got %f", minL)
	}
	if math.Abs(maxL-50) > 0.001 {
		t.Errorf("max: want 50, got %f", maxL)
	}
	// p50 of [10,20,30,40,50] (5 elements) → idx = 0.5*4 = 2.0 → 30
	if math.Abs(p50-30) > 0.001 {
		t.Errorf("p50: want 30, got %f", p50)
	}
	// p95 → idx = 0.95*4 = 3.8 → 40 + 0.8*(50-40) = 48
	if math.Abs(p95-48) > 0.5 {
		t.Errorf("p95: want ~48, got %f", p95)
	}
	// p99 → idx = 0.99*4 = 3.96 → 40 + 0.96*(50-40) = 49.6
	if math.Abs(p99-49.6) > 0.5 {
		t.Errorf("p99: want ~49.6, got %f", p99)
	}
}

func TestComputeLatency_Unsorted(t *testing.T) {
	e := New()
	// Write values in reverse order — computeLatency must sort before percentile.
	seedLatBuf(e, 100, 50, 25, 75)

	minL, maxL, _, _, _ := e.computeLatency()

	if math.Abs(minL-25) > 0.001 {
		t.Errorf("min: want 25, got %f", minL)
	}
	if math.Abs(maxL-100) > 0.001 {
		t.Errorf("max: want 100, got %f", maxL)
	}
}

func TestComputeLatency_DoesNotMutateOriginalSlice(t *testing.T) {
	e := New()
	seedLatBuf(e, 5, 3, 8, 1, 9, 2)

	// Call twice — results must be identical (no in-place sort of shared data).
	min1, max1, p50a, p95a, p99a := e.computeLatency()
	min2, max2, p50b, p95b, p99b := e.computeLatency()

	if min1 != min2 || max1 != max2 || p50a != p50b || p95a != p95b || p99a != p99b {
		t.Errorf("repeated calls returned different results: "+
			"first=(min=%f max=%f p50=%f p95=%f p99=%f) "+
			"second=(min=%f max=%f p50=%f p95=%f p99=%f)",
			min1, max1, p50a, p95a, p99a,
			min2, max2, p50b, p95b, p99b)
	}
}

func TestComputeLatency_P99GeP95GeP50(t *testing.T) {
	e := New()
	// Seed a realistic-ish latency distribution (ms).
	vals := []float64{5, 6, 7, 8, 9, 10, 11, 12, 15, 20, 25, 50, 100, 200, 300}
	seedLatBuf(e, vals...)

	_, _, p50, p95, p99 := e.computeLatency()

	if p99 < p95 {
		t.Errorf("p99 (%f) should be >= p95 (%f)", p99, p95)
	}
	if p95 < p50 {
		t.Errorf("p95 (%f) should be >= p50 (%f)", p95, p50)
	}
}
