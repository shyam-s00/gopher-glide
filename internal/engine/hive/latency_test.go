package hive

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
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

// ── 1.4.1i — lock-free latencyBuf integration tests ──────────────────────────

// TestLatencyBuf_RecordAndSnapshot verifies that a value written via the real
// recordLatency() hot-path is visible in computeLatency().
func TestLatencyBuf_RecordAndSnapshot(t *testing.T) {
	e := New()
	// recordLatency internally uses float64(d) / float64(time.Millisecond), so
	// 50ms → 50.0 ms stored in the ring buffer.
	e.recordLatency(0, 50*time.Millisecond)

	minL, maxL, p50, p95, p99 := e.computeLatency()
	for name, got := range map[string]float64{"min": minL, "max": maxL, "p50": p50, "p95": p95, "p99": p99} {
		if math.Abs(got-50.0) > 0.001 {
			t.Errorf("%s: want 50.0 ms, got %f", name, got)
		}
	}
}

// TestLatencyBuf_WrapAround confirms that, once the ring is full, new writes
// overwrite the oldest entry (capacity cap respected, no unbounded growth).
func TestLatencyBuf_WrapAround(t *testing.T) {
	const cap = 4
	e := New()
	// Replace the default buffer with a tiny 4-slot ring.
	smallBuf := newLatencyBuf(cap)
	e.latBuf.Store(&smallBuf)

	// Write cap+1 values: 1.0, 2.0, 3.0, 4.0, 5.0 ms
	// After wrap, slot 0 is overwritten with 5.0; slots 1-3 hold 2-4.
	for i := 1; i <= cap+1; i++ {
		e.recordLatency(0, time.Duration(i)*time.Millisecond)
	}

	lb := e.latBuf.Load()
	n := lb.n.Load() // total writes = cap+1 = 5
	if n != int64(cap+1) {
		t.Errorf("n: want %d, got %d", cap+1, n)
	}

	// computeLatency reads min(n, cap) = cap entries — no single read exceeds cap.
	minL, maxL, _, _, _ := e.computeLatency()

	// After wrap: the ring holds values {2, 3, 4, 5} (not the original 1.0).
	// min must be ≥ 2.0 (slot 0 was overwritten by 5.0).
	if minL < 2.0-0.001 {
		t.Errorf("min after wrap: want ≥ 2.0, got %f (oldest entry not overwritten)", minL)
	}
	if maxL < 5.0-0.001 {
		t.Errorf("max after wrap: want ≥ 5.0, got %f (newest entry missing)", maxL)
	}
}

// TestLatencyBuf_ConcurrentAppends fires many goroutines simultaneously writing
// to the ring buffer via recordLatency(). Passes under -race with no data races.
func TestLatencyBuf_ConcurrentAppends(t *testing.T) {
	const goroutines = 64
	const writesEach = 50
	e := New()

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < writesEach; i++ {
				e.recordLatency(id%numShards, time.Duration(i+1)*time.Millisecond)
			}
		}(g)
	}
	wg.Wait()

	lb := e.latBuf.Load()
	totalWrites := int64(goroutines * writesEach)
	n := lb.n.Load()
	if n != totalWrites {
		t.Errorf("n: want %d total writes, got %d", totalWrites, n)
	}

	// At least some latency must be non-zero.
	minL, maxL, _, _, _ := e.computeLatency()
	if maxL <= 0 {
		t.Errorf("max latency after concurrent writes should be > 0, got %f", maxL)
	}
	if minL < 0 {
		t.Errorf("min latency should be ≥ 0, got %f", minL)
	}
}

// TestLatencyBuf_LiveEngine_Consistency polls computeLatency() repeatedly
// during a live RunStages run and asserts that min ≤ p50 ≤ p99 ≤ max at
// every sample point.
func TestLatencyBuf_LiveEngine_Consistency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	e := New()
	cfg := hiveStage(600*time.Millisecond, 20)

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = e.RunStages(context.Background(), cfg, specsFor(srv.URL))
	}()

	// Poll computeLatency until the run finishes.
	violations := 0
	for {
		minL, maxL, p50, _, p99 := e.computeLatency()
		// Only validate once we have at least one sample (all zeros before first write).
		if maxL > 0 {
			if minL > p50 {
				t.Errorf("invariant broken: min (%f) > p50 (%f)", minL, p50)
				violations++
			}
			if p50 > p99 {
				t.Errorf("invariant broken: p50 (%f) > p99 (%f)", p50, p99)
				violations++
			}
			if p99 > maxL {
				t.Errorf("invariant broken: p99 (%f) > max (%f)", p99, maxL)
				violations++
			}
		}
		if violations >= 3 {
			break // avoid flooding output
		}
		select {
		case <-runDone:
			return
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// ── buffer capacity bounds ────────────────────────────────────────────────────

// TestLatencyBuf_MinCap verifies that RunStages always allocates at least
// minLatencyBufCap slots even for very short / low-RPS test plans.
func TestLatencyBuf_MinCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	_ = e.RunStages(context.Background(), singleStageCfg(10*time.Millisecond, 1), specsFor(srv.URL))

	lb := e.latBuf.Load()
	if lb == nil {
		t.Fatal("latBuf is nil after RunStages")
	}
	if len(lb.buf) < minLatencyBufCap {
		t.Errorf("expected cap >= %d, got %d", minLatencyBufCap, len(lb.buf))
	}
}

// TestLatencyBuf_MaxCap verifies that RunStages never allocates more than
// maxLatencyBufCap slots even for extremely high-RPS, long-duration test plans
// (the scenario that would otherwise allocate gigabytes).
func TestLatencyBuf_MaxCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	// 100 000 RPS × 3 600 s would request 360 M slots ≈ 2.9 GB without the cap.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — we only care about the buffer allocation
	_ = e.RunStages(ctx, singleStageCfg(1*time.Hour, 100_000), specsFor(srv.URL))

	lb := e.latBuf.Load()
	if lb == nil {
		t.Fatal("latBuf is nil after RunStages")
	}
	if len(lb.buf) > maxLatencyBufCap {
		t.Errorf("expected cap <= %d, got %d — OOM guard failed", maxLatencyBufCap, len(lb.buf))
	}
}
