package hive

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// makeURLSpecs returns n identical GET specs pointing at the given URL.
func makeURLSpecs(n int, url string) []httpreader.RequestSpec {
	specs := make([]httpreader.RequestSpec, n)
	for i := range specs {
		specs[i] = httpreader.RequestSpec{Method: http.MethodGet, URL: url}
	}
	return specs
}

// waitForActors waits until activeActors reaches zero or the deadline passes.
func waitForActors(t *testing.T, e *Engine, deadline time.Duration) {
	t.Helper()
	timeout := time.Now().Add(deadline)
	for e.activeActors.Load() > 0 {
		if time.Now().After(timeout) {
			t.Fatalf("timed out waiting for actors to finish (still %d active)", e.activeActors.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ── dispatch: zero / empty ────────────────────────────────────────────────────

func TestHatchery_Dispatch_ZeroCount_SpawnsNothing(t *testing.T) {
	srv := newTestServer(http.StatusOK, "")
	defer srv.Close()
	e := New()
	h := &hatchery{e: e}
	idx := 0
	h.dispatch(context.Background(), SpawnManifest{Count: 0, Duration: time.Second, SpecIndex: 0}, makeURLSpecs(1, srv.URL), &idx)
	if e.activeActors.Load() != 0 {
		t.Fatalf("expected 0 active actors, got %d", e.activeActors.Load())
	}
}

func TestHatchery_Dispatch_EmptySpecs_SpawnsNothing(t *testing.T) {
	e := New()
	h := &hatchery{e: e}
	idx := 0
	h.dispatch(context.Background(), SpawnManifest{Count: 5, Duration: time.Second}, []httpreader.RequestSpec{}, &idx)
	if e.activeActors.Load() != 0 {
		t.Fatalf("expected 0 active actors, got %d", e.activeActors.Load())
	}
}

// ── dispatch: MaxActors safeguard ────────────────────────────────────────────

// TestHatchery_Dispatch_AtMaxActors_SkipsSpawning pre-saturates activeActors
// to the maxActiveActors ceiling and verifies the Hatchery refuses to launch
// any further Actor goroutines — protecting the host from OOM under the
// Arrival Rate model (slow targets would otherwise pile up parked goroutines).
func TestHatchery_Dispatch_AtMaxActors_SkipsSpawning(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0

	// Saturate the engine to the ceiling before dispatching.
	e.activeActors.Store(maxActiveActors)

	const count = 10
	h.dispatch(context.Background(), SpawnManifest{Count: count, Duration: 50 * time.Millisecond, SpecIndex: 0}, makeURLSpecs(1, srv.URL), &idx)

	// Give any wrongly-spawned actor a moment to hit the test server.
	time.Sleep(100 * time.Millisecond)

	if served.Load() != 0 {
		t.Fatalf("expected 0 requests while at the MaxActors ceiling, got %d", served.Load())
	}
	if got := e.activeActors.Load(); got != maxActiveActors {
		t.Fatalf("expected activeActors to remain at the ceiling %d, got %d", maxActiveActors, got)
	}
	if idx != count {
		t.Fatalf("expected spawnIdx to advance by %d even when spawns are skipped, got %d", count, idx)
	}
}

// TestHatchery_Dispatch_BelowMaxActors_SpawnsNormally verifies the safeguard
// only engages once the ceiling is reached — normal dispatch is unaffected
// while headroom remains.
func TestHatchery_Dispatch_BelowMaxActors_SpawnsNormally(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0

	// Plenty of headroom below the ceiling.
	e.activeActors.Store(maxActiveActors - 10)

	const count = 5
	h.dispatch(context.Background(), SpawnManifest{Count: count, Duration: time.Second, SpecIndex: 0}, makeURLSpecs(1, srv.URL), &idx)

	deadline := time.Now().Add(3 * time.Second)
	for served.Load() < count {
		if time.Now().After(deadline) {
			t.Fatalf("expected %d requests, got %d after timeout", count, served.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if served.Load() != count {
		t.Fatalf("expected exactly %d requests, got %d", count, served.Load())
	}
}

// ── dispatch: correct actor count ────────────────────────────────────────────

func TestHatchery_Dispatch_SmallCount_AllActorsSpawned(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0
	const count = 5
	h.dispatch(context.Background(), SpawnManifest{Count: count, Duration: time.Second, SpecIndex: 0}, makeURLSpecs(1, srv.URL), &idx)

	// All actors should eventually finish.
	deadline := time.Now().Add(3 * time.Second)
	for served.Load() < count {
		if time.Now().After(deadline) {
			t.Fatalf("expected %d requests, got %d after timeout", count, served.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if served.Load() != count {
		t.Fatalf("expected exactly %d requests, got %d", count, served.Load())
	}
}

func TestHatchery_Dispatch_ExactlyTicksPerSec_AllActorsSpawned(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0
	// 100 actors over 1 second = 1 per 10ms tick
	const count = int32(100)
	h.dispatch(context.Background(), SpawnManifest{Count: int(count), Duration: time.Second}, makeURLSpecs(1, srv.URL), &idx)

	deadline := time.Now().Add(5 * time.Second)
	for served.Load() < count {
		if time.Now().After(deadline) {
			t.Fatalf("expected %d requests, got %d", count, served.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if served.Load() != count {
		t.Fatalf("expected exactly %d requests, got %d", count, served.Load())
	}
}

// ── dispatch: remainder math ──────────────────────────────────────────────────

func TestHatchery_Dispatch_RemainderDistributed_Correctly(t *testing.T) {
	// count=7, duration=1s (100 ticks) → batchSize=0, remainder=7.
	// Ticks 0-6 each spawn 1; ticks 7+ spawn 0 (but loop exits at spawned==7).
	// Total wall time ≈ 7 * 10ms = 70ms.
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0
	const count = 7
	h.dispatch(context.Background(), SpawnManifest{Count: count, Duration: time.Second}, makeURLSpecs(1, srv.URL), &idx)

	deadline := time.Now().Add(3 * time.Second)
	for served.Load() < count {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: expected %d, got %d", count, served.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if served.Load() != count {
		t.Fatalf("expected %d served, got %d", count, served.Load())
	}
}

// ── dispatch: spawnIdx drives shard assignment ────────────────────────────────

func TestHatchery_Dispatch_SpawnIdx_Advances(t *testing.T) {
	srv := newTestServer(http.StatusOK, "")
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0
	const count = 3
	h.dispatch(context.Background(), SpawnManifest{Count: count, Duration: time.Second}, makeURLSpecs(1, srv.URL), &idx)
	waitForActors(t, e, 3*time.Second)

	if idx != count {
		t.Fatalf("expected spawnIdx=%d after dispatch, got %d", count, idx)
	}
}

// ── dispatch: journey runs all specs ─────────────────────────────────────────

func TestHatchery_Dispatch_JourneyRunsAllSpecs(t *testing.T) {
	// In journey mode each spawned Actor goroutine executes ALL specs in the
	// slice sequentially.  With Count=2 and a 2-spec journey both servers
	// must be hit by every actor (2 actors × 2 specs = 4 total hits).
	var hit0, hit1 atomic.Int32
	srv0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit0.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit1.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv0.Close()
	defer srv1.Close()

	specs := []httpreader.RequestSpec{
		{Method: http.MethodGet, URL: srv0.URL},
		{Method: http.MethodGet, URL: srv1.URL},
	}

	e := New()
	h := &hatchery{e: e}
	idx := 0

	// Count=2 actors × 2-spec journey → srv0 hit 2×, srv1 hit 2×.
	h.dispatch(context.Background(), SpawnManifest{Count: 2, Duration: time.Second}, specs, &idx)
	waitForActors(t, e, 3*time.Second)

	if hit0.Load() == 0 || hit1.Load() == 0 {
		t.Fatalf("expected both servers hit; srv0=%d srv1=%d", hit0.Load(), hit1.Load())
	}
}

// ── dispatch: activeActors lifecycle ─────────────────────────────────────────

func TestHatchery_Dispatch_ActiveActors_ReturnToZero(t *testing.T) {
	srv := newTestServer(http.StatusOK, "")
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0

	h.dispatch(context.Background(), SpawnManifest{Count: 4, Duration: time.Second}, makeURLSpecs(1, srv.URL), &idx)
	waitForActors(t, e, 3*time.Second)

	if e.activeActors.Load() != 0 {
		t.Fatalf("expected 0 active actors after completion, got %d", e.activeActors.Load())
	}
}

func TestHatchery_Dispatch_ActiveActors_NonNegative(t *testing.T) {
	srv := newTestServer(http.StatusOK, "")
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0

	go h.dispatch(context.Background(), SpawnManifest{Count: 5, Duration: time.Second}, makeURLSpecs(1, srv.URL), &idx)

	// Poll for negative value (should never happen).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if e.activeActors.Load() < 0 {
			t.Fatalf("activeActors went negative: %d", e.activeActors.Load())
		}
		time.Sleep(1 * time.Millisecond)
	}
}

// ── dispatch: metrics counters ────────────────────────────────────────────────

func TestHatchery_Dispatch_TotalRequests_Incremented(t *testing.T) {
	srv := newTestServer(http.StatusOK, "")
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0
	const count = 3
	h.dispatch(context.Background(), SpawnManifest{Count: count, Duration: time.Second}, makeURLSpecs(1, srv.URL), &idx)
	waitForActors(t, e, 3*time.Second)

	if got := e.counters.loadTotalRequests(); got != count {
		t.Fatalf("expected totalRequests=%d, got %d", count, got)
	}
}

func TestHatchery_Dispatch_SuccessCount_Incremented(t *testing.T) {
	srv := newTestServer(http.StatusOK, "")
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0
	const count = 3
	h.dispatch(context.Background(), SpawnManifest{Count: count, Duration: time.Second}, makeURLSpecs(1, srv.URL), &idx)
	waitForActors(t, e, 3*time.Second)

	if got := e.counters.loadSuccessCount(); got != count {
		t.Fatalf("expected successCount=%d, got %d", count, got)
	}
}

func TestHatchery_Dispatch_FailureCount_Incremented(t *testing.T) {
	srv := newTestServer(http.StatusInternalServerError, "error")
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0
	const count = 2
	h.dispatch(context.Background(), SpawnManifest{Count: count, Duration: time.Second}, makeURLSpecs(1, srv.URL), &idx)
	waitForActors(t, e, 3*time.Second)

	if got := e.counters.loadFailureCount(); got != count {
		t.Fatalf("expected failureCount=%d, got %d", count, got)
	}
}

// ── dispatch: context cancel stops mid-dispatch ───────────────────────────────

func TestHatchery_Dispatch_ContextCancel_StopsMidway(t *testing.T) {
	// Hang server so actors stay in flight while we cancel.
	hangCh := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hangCh:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { close(hangCh); srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())

	e := New()
	h := &hatchery{e: e}
	idx := 0

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Large count so dispatch runs for ~1s; cancel will interrupt it.
		h.dispatch(ctx, SpawnManifest{Count: 1000, Duration: time.Second}, makeURLSpecs(1, srv.URL), &idx)
	}()

	// Let a few ticks fire then cancel.
	time.Sleep(25 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not return after context cancellation")
	}
}

// ── run: closes on cancelled context ─────────────────────────────────────────

func TestHatchery_Run_ContextCancel_Returns(t *testing.T) {
	srv := newTestServer(http.StatusOK, "")
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	manifestCh := make(chan SpawnManifest, 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- h.run(ctx, manifestCh, makeURLSpecs(1, srv.URL))
	}()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return after context cancellation")
	}
}

func TestHatchery_Run_ClosedChannel_Returns(t *testing.T) {
	e := New()
	h := &hatchery{e: e}
	manifestCh := make(chan SpawnManifest)
	close(manifestCh) // closed immediately

	done := make(chan error, 1)
	go func() {
		done <- h.run(context.Background(), manifestCh, makeURLSpecs(1, "http://localhost"))
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return after channel close")
	}
}

// ── run: processes multiple manifests ────────────────────────────────────────

func TestHatchery_Run_MultipleManifests_AllActorsSpawned(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	manifestCh := make(chan SpawnManifest, 2)
	manifestCh <- SpawnManifest{Count: 3, Duration: time.Second, SpecIndex: 0}
	manifestCh <- SpawnManifest{Count: 2, Duration: time.Second, SpecIndex: 0}
	close(manifestCh)

	if err := h.run(context.Background(), manifestCh, makeURLSpecs(1, srv.URL)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	waitForActors(t, e, 5*time.Second)

	const wantTotal = 5
	if served.Load() != wantTotal {
		t.Fatalf("expected %d requests, got %d", wantTotal, served.Load())
	}
}

// ── micro-batching timing ─────────────────────────────────────────────────────

func TestHatchery_Dispatch_SpacingBoundedWithinWindow(t *testing.T) {
	// Verify that dispatching count=5 over 1s takes at most 1s.
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0
	start := time.Now()
	h.dispatch(context.Background(), SpawnManifest{Count: 5, Duration: time.Second}, makeURLSpecs(1, srv.URL), &idx)
	elapsed := time.Since(start)

	// 5 actors over 1s window: 1 per tick at 10ms each → ~50ms total
	if elapsed > time.Second {
		t.Fatalf("dispatch took too long: %v (expected ≤ 1s for count=5)", elapsed)
	}
}

// ── variable-duration dispatch: the core of 1.2.13 ───────────────────────────

func TestHatchery_Dispatch_400ms_Duration_BoundedWithinWindow(t *testing.T) {
	// count=5, duration=400ms → 40 ticks. batchSize=0, remainder=5.
	// Ticks 0-4 spawn 1 each → total wall time ≈ 5*10ms = 50ms.
	// Critically: must NOT take 1 full second (the old hardcoded assumption).
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0

	start := time.Now()
	h.dispatch(context.Background(), SpawnManifest{Count: 5, Duration: 400 * time.Millisecond}, makeURLSpecs(1, srv.URL), &idx)
	elapsed := time.Since(start)

	// All actors must be spawned.
	waitForActors(t, e, 3*time.Second)
	if served.Load() != 5 {
		t.Fatalf("expected 5 requests, got %d", served.Load())
	}
	// Dispatch must complete well inside the 400ms window, not bleed into a full second.
	if elapsed > 400*time.Millisecond {
		t.Fatalf("dispatch took %v; expected ≤ 400ms for 5 actors over 400ms window", elapsed)
	}
}

func TestHatchery_Dispatch_SubSecond_NoTemporalBleeding(t *testing.T) {
	// Simulate two back-to-back 400ms stages.
	// With the OLD hardcoded 1s assumption, total time would be ~2s.
	// With the NEW Duration-driven math, total time must be ≤ ~800ms.
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	specs := makeURLSpecs(1, srv.URL)
	idx := 0

	start := time.Now()
	// Stage 1: 5 actors over 400ms
	h.dispatch(context.Background(), SpawnManifest{Count: 5, Duration: 400 * time.Millisecond}, specs, &idx)
	// Stage 2: 5 actors over 400ms
	h.dispatch(context.Background(), SpawnManifest{Count: 5, Duration: 400 * time.Millisecond}, specs, &idx)
	elapsed := time.Since(start)

	waitForActors(t, e, 5*time.Second)
	if served.Load() != 10 {
		t.Fatalf("expected 10 total requests, got %d", served.Load())
	}
	// Two 400ms windows should not exceed 1s total (generous 200ms headroom for CI jitter).
	if elapsed > 1200*time.Millisecond {
		t.Fatalf("two 400ms dispatches took %v; expected ≤ 1.2s (no temporal bleeding)", elapsed)
	}
}

func TestHatchery_Dispatch_TinyDuration_ClampedToOneTick(t *testing.T) {
	// duration=5ms < hatcheryTick(10ms) → numTicks is clamped to 1.
	// All count=3 actors must be spawned in the single tick.
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0

	h.dispatch(context.Background(), SpawnManifest{Count: 3, Duration: 5 * time.Millisecond}, makeURLSpecs(1, srv.URL), &idx)
	waitForActors(t, e, 3*time.Second)

	if served.Load() != 3 {
		t.Fatalf("expected 3 requests, got %d", served.Load())
	}
}

func TestHatchery_Dispatch_ZeroDuration_FallsBackToOneTick(t *testing.T) {
	// Zero/negative Duration must fall back to a 1-second window (safe default).
	// The important thing is it must NOT panic or block forever.
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	idx := 0

	h.dispatch(context.Background(), SpawnManifest{Count: 3, Duration: 0}, makeURLSpecs(1, srv.URL), &idx)
	waitForActors(t, e, 5*time.Second)

	if served.Load() != 3 {
		t.Fatalf("expected 3 requests, got %d", served.Load())
	}
}

// ── concurrent safety ─────────────────────────────────────────────────────────

func TestHatchery_Dispatch_ConcurrentCalls_NoRace(t *testing.T) {
	srv := newTestServer(http.StatusOK, "")
	defer srv.Close()

	e := New()
	h := &hatchery{e: e}
	specs := makeURLSpecs(1, srv.URL)

	var wg atomic.Int32
	const goroutines = 4
	wg.Store(goroutines)

	idxes := make([]int, goroutines)
	for i := 0; i < goroutines; i++ {
		go func(g int) {
			defer wg.Add(-1)
			h.dispatch(context.Background(), SpawnManifest{Count: 3, Duration: time.Second}, specs, &idxes[g])
		}(i)
	}

	deadline := time.Now().Add(5 * time.Second)
	for wg.Load() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for concurrent dispatches")
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitForActors(t, e, 3*time.Second)
}

// ── benchmark ─────────────────────────────────────────────────────────────────

// BenchmarkHatchery_Dispatch measures dispatch throughput.
// Run with: go test -bench=BenchmarkHatchery -benchtime=5s ./internal/engine/hive/
func BenchmarkHatchery_Dispatch(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	specs := []httpreader.RequestSpec{{Method: http.MethodGet, URL: srv.URL}}
	e := New()
	h := &hatchery{e: e}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := 0
		h.dispatch(context.Background(), SpawnManifest{Count: 10, Duration: time.Second}, specs, &idx)
		waitR := time.Now().Add(3 * time.Second)
		for e.activeActors.Load() > 0 && time.Now().Before(waitR) {
			time.Sleep(time.Millisecond)
		}
	}
}

// BenchmarkHatchery_HighRPS measures dispatch at 500 RPS target.
func BenchmarkHatchery_HighRPS(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	specs := []httpreader.RequestSpec{{Method: http.MethodGet, URL: fmt.Sprintf("%s", srv.URL)}}
	e := New()
	h := &hatchery{e: e}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := 0
		h.dispatch(context.Background(), SpawnManifest{Count: 500, Duration: time.Second}, specs, &idx)
		deadline := time.Now().Add(5 * time.Second)
		for e.activeActors.Load() > 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
}
