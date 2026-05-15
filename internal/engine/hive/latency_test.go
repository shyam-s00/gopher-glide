package hive

import (
	"math"
	"testing"
)

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
	e.latencies = []float64{37}

	minL, maxL, p50, p95, p99 := e.computeLatency()

	for name, got := range map[string]float64{
		"min": minL, "max": maxL, "p50": p50, "p95": p95, "p99": p99,
	} {
		if got != 37 {
			t.Errorf("%s: want 37, got %f", name, got)
		}
	}
}

func TestComputeLatency_Values(t *testing.T) {
	e := New()
	e.latencies = []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}

	minL, maxL, p50, p95, p99 := e.computeLatency()

	if minL != 10 {
		t.Errorf("min: want 10, got %f", minL)
	}
	if maxL != 100 {
		t.Errorf("max: want 100, got %f", maxL)
	}
	if p50 < 50 || p50 > 60 {
		t.Errorf("p50: want in [50,60], got %f", p50)
	}
	if p95 < 90 {
		t.Errorf("p95: want ≥ 90, got %f", p95)
	}
	if p99 < p95 {
		t.Errorf("p99 (%f) must be ≥ p95 (%f)", p99, p95)
	}
}

func TestComputeLatency_Unsorted(t *testing.T) {
	// latencies are recorded in arrival order (unsorted); computeLatency must sort.
	e := New()
	e.latencies = []float64{90, 10, 50, 30, 70} // unsorted

	minL, maxL, _, _, _ := e.computeLatency()

	if minL != 10 {
		t.Errorf("min: want 10 from unsorted input, got %f", minL)
	}
	if maxL != 90 {
		t.Errorf("max: want 90 from unsorted input, got %f", maxL)
	}
}

func TestComputeLatency_DoesNotMutateOriginalSlice(t *testing.T) {
	// computeLatency must sort a copy — the Engine's own latencies slice must
	// remain in insertion order so subsequent appends stay correct.
	e := New()
	e.latencies = []float64{90, 10, 50}

	e.computeLatency()

	// Original slice must not have been sorted in-place.
	if e.latencies[0] != 90 {
		t.Errorf("original latencies[0]: want 90 (insertion order), got %f", e.latencies[0])
	}
}

func TestComputeLatency_P99GeP95GeP50(t *testing.T) {
	e := New()
	// Uniformly distributed sample: percentile ordering must hold.
	e.latencies = make([]float64, 100)
	for i := range e.latencies {
		e.latencies[i] = float64(i + 1) // 1..100
	}

	_, _, p50, p95, p99 := e.computeLatency()

	if p50 > p95 {
		t.Errorf("p50 (%f) must be ≤ p95 (%f)", p50, p95)
	}
	if p95 > p99 {
		t.Errorf("p95 (%f) must be ≤ p99 (%f)", p95, p99)
	}
}
