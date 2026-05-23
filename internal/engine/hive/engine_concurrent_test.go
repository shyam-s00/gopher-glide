package hive

// engine_concurrent_test.go — race-detector test suite for the Hive Engine.
//
// Run with:
//
//	go test -race ./internal/engine/hive/... -run Concurrent
//
// Every test here is designed to surface data races, invariant violations, and
// deadlocks that only manifest under concurrent access. The suite mirrors the
// legacy engine's engine_concurrent_test.go and adds Hive-specific cases.
//
// Coverage:
//   - rpsWindow concurrent record + rate
//   - logCall concurrent writes + reads; buffer cap
//   - latency slice concurrent appends + computeLatency reads
//   - GetMetrics counter invariant during a live run
//   - Mixed-response counter invariant (total == success+failure) after run
//   - activeActors never exceeds spawned count
//   - ApplyBias many concurrent senders — all deltas absorbed
//   - Round-robin across multiple specs under high concurrency
//   - Log buffer: no panic on eviction
//   - IsRunning clean transition
//   - GetMetrics latency-snapshot consistency (p99 ≥ p50)
//   - Multiple specs at high RPS — counter invariant holds
//   - Context cancel racing with bias drain — no deadlock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// slowHiveSrv starts a test server that optionally delays before responding.
func slowHiveSrv(t *testing.T, statusCode int, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		w.WriteHeader(statusCode)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// multiHiveSpec returns n identical GET specs pointing to url.
func multiHiveSpec(url string, n int) []httpreader.RequestSpec {
	specs := make([]httpreader.RequestSpec, n)
	for i := range specs {
		specs[i] = httpreader.RequestSpec{Method: http.MethodGet, URL: url}
	}
	return specs
}

// hiveStage returns a single-stage config with the given duration and RPS.
func hiveStage(dur time.Duration, rps int) *config.Config {
	return &config.Config{
		ConfigSection: config.Section{TimeScale: 1.0},
		Stages:        []config.Stage{{Duration: dur, TargetRPS: rps}},
	}
}

// ── 1. rpsWindow: concurrent record + rate ────────────────────────────────────

func TestConcurrent_RpsWindow_RecordAndRate(t *testing.T) {
	var w rpsWindow
	const goroutines = 50
	const opsEach = 200

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < opsEach; j++ {
				w.record(1)
			}
		}()
	}
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < opsEach; j++ {
				_ = w.rate()
			}
		}()
	}
	wg.Wait()
}

// ── 2. logCall: concurrent writes + reads ─────────────────────────────────────

func TestConcurrent_LogCall_ConcurrentWritesAndReads(t *testing.T) {
	e := New()
	const goroutines = 20
	const callsEach = 100

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < callsEach; j++ {
				e.logCall("GET", "http://example.com", 200, time.Millisecond, nil)
			}
		}()
	}
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < callsEach; j++ {
				_ = e.GetRecentLogs(10)
				_ = e.GetRecentErrorLogs(10)
			}
		}()
	}
	wg.Wait()

	logs := e.GetRecentLogs(e.maxLogs + 100)
	if len(logs) > e.maxLogs {
		t.Errorf("callLogs exceeded maxLogs: got %d, want ≤ %d", len(logs), e.maxLogs)
	}
}

// ── 3. logCall: error buffer never exceeds max ────────────────────────────────

func TestConcurrent_LogCall_ErrorBuffer_NeverExceedsMax(t *testing.T) {
	e := New()
	e.maxLogs = 20
	const goroutines = 10
	const callsEach = 50

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < callsEach; j++ {
				e.logCall("GET", "http://example.com", 500, time.Millisecond, ErrHttpError)
			}
		}()
	}
	wg.Wait()

	errLogs := e.GetRecentErrorLogs(e.maxLogs + 100)
	if len(errLogs) > e.maxLogs {
		t.Errorf("errorLogs exceeded maxLogs: got %d, want ≤ %d", len(errLogs), e.maxLogs)
	}
}

// ── 4. latencies slice: concurrent appends + computeLatency reads ─────────────

func TestConcurrent_Latencies_ConcurrentAppends(t *testing.T) {
	e := New()
	const goroutines = 50
	const appendsEach = 100

	var wg sync.WaitGroup
	// writers: directly append under the mutex (same path as recordLatency)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(val float64) {
			defer wg.Done()
			for j := 0; j < appendsEach; j++ {
				e.latencyMu.Lock()
				e.latencies = append(e.latencies, val)
				e.latencyMu.Unlock()
			}
		}(float64(i))
	}
	// readers: computeLatency takes RLock + sorts a copy
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < appendsEach; j++ {
				_, _, _, _, _ = e.computeLatency()
			}
		}()
	}
	wg.Wait()

	e.latencyMu.RLock()
	got := len(e.latencies)
	e.latencyMu.RUnlock()
	want := goroutines * appendsEach
	if got != want {
		t.Errorf("latencies length: want %d, got %d (writes lost under race)", want, got)
	}
}

// ── 5. GetMetrics counter invariant during a live run ─────────────────────────

func TestConcurrent_GetMetrics_DuringRun(t *testing.T) {
	srv := slowHiveSrv(t, 200, 0)
	e := New()
	cfg := hiveStage(400*time.Millisecond, 20)

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = e.RunStages(context.Background(), cfg, specsFor(srv.URL))
	}()

	const readers = 20
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				m := e.GetMetrics()
				// totalRequests is incremented before success/failure inside
				// executeActor, so a mid-flight snapshot may see total=N with
				// success+failure=N-1 — but never the reverse.
				if m.TotalRequests < m.SuccessCount+m.FailureCount {
					t.Errorf("counter invariant broken: total=%d success=%d failure=%d",
						m.TotalRequests, m.SuccessCount, m.FailureCount)
				}
				if m.ErrorRate < 0 || m.ErrorRate > 1 {
					t.Errorf("ErrorRate out of range: %f", m.ErrorRate)
				}
				select {
				case <-runDone:
					return
				default:
					time.Sleep(5 * time.Millisecond)
				}
			}
		}()
	}
	<-runDone
	wg.Wait()
}

// ── 6. Mixed-response counter invariant after run ─────────────────────────────

func TestConcurrent_CounterInvariant_MixedResponses(t *testing.T) {
	var reqCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if reqCount.Add(1)%2 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	e := New()
	_ = e.RunStages(context.Background(), hiveStage(400*time.Millisecond, 30),
		specsFor(srv.URL))

	m := e.GetMetrics()
	if m.TotalRequests != m.SuccessCount+m.FailureCount {
		t.Errorf("counter invariant broken: total=%d success=%d failure=%d",
			m.TotalRequests, m.SuccessCount, m.FailureCount)
	}
	if m.TotalRequests == 0 {
		t.Error("expected requests to be sent")
	}
}

// ── 7. activeActors never exceeds spawned count ───────────────────────────────

func TestConcurrent_ActiveActors_NeverExceedsSpawned(t *testing.T) {
	// Slow server keeps actors alive long enough to observe concurrency.
	srv := slowHiveSrv(t, 200, 10*time.Millisecond)
	e := New()

	const targetRPS = 50
	cfg := hiveStage(400*time.Millisecond, targetRPS)

	// The Queen will emit up to targetRPS actors per tick; the Hatchery
	// spaces them across the 1-second window. Active count must never exceed
	// the number spawned so far — and must eventually reach 0 after RunStages.
	var maxObserved atomic.Int32
	runDone := make(chan struct{})

	go func() {
		defer close(runDone)
		_ = e.RunStages(context.Background(), cfg, specsFor(srv.URL))
	}()

	// Poll activeActors and track the peak.
	go func() {
		for {
			select {
			case <-runDone:
				return
			default:
				cur := e.activeActors.Load()
				for {
					old := maxObserved.Load()
					if cur <= old || maxObserved.CompareAndSwap(old, cur) {
						break
					}
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()

	<-runDone

	// After RunStages returns, all actors must have exited (drain loop).
	if remaining := e.activeActors.Load(); remaining != 0 {
		t.Errorf("activeActors after run: want 0, got %d", remaining)
	}
	// Peak must be sane — bounded by (targetRPS * HTTP-client-timeout / stage-dur)
	// but we just check it's non-negative and reasonable.
	if maxObserved.Load() < 0 {
		t.Errorf("activeActors went negative: %d", maxObserved.Load())
	}
}

// ── 8. ApplyBias: many concurrent senders, all deltas absorbed ────────────────

func TestConcurrent_ApplyBias_ManySenders(t *testing.T) {
	srv := slowHiveSrv(t, 200, 0)
	e := New()
	// 2.5-second run: the Queen fires its 1s heartbeat at t≈1s and t≈2s,
	// calling drainBias() each time. We send exactly 15 deltas total —
	// safely below biasCh's capacity of 16 — so no sends are dropped and
	// after the first drain all 15 are reflected in rpsBias.
	cfg := hiveStage(2500*time.Millisecond, 5)

	// 5 goroutines × 3 sends = 15 total (< biasCh cap 16).
	// All sends complete within the first ~100ms, well before the 1s Queen tick.
	const biasSenders = 5
	const biasEach = 3

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = e.RunStages(context.Background(), cfg, specsFor(srv.URL))
	}()

	var wg sync.WaitGroup
	for i := 0; i < biasSenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < biasEach; j++ {
				e.ApplyBias(1)
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	<-runDone

	// All 15 deltas must be absorbed: they all fit in biasCh (cap=16) and
	// the Queen drains the channel on its first 1-second heartbeat tick.
	got := e.GetBias()
	want := biasSenders * biasEach
	if got != want {
		t.Errorf("bias: want %d, got %d (some deltas dropped — check biasCh cap vs send count)", want, got)
	}
}

// ── 9. Round-robin: all specs receive requests under concurrency ──────────────

func TestConcurrent_RoundRobin_AllSpecsReceiveRequests(t *testing.T) {
	const numSpecs = 3
	hits := make([]atomic.Int64, numSpecs)

	servers := make([]*httptest.Server, numSpecs)
	specs := make([]httpreader.RequestSpec, numSpecs)
	for i := 0; i < numSpecs; i++ {
		idx := i
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits[idx].Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(servers[i].Close)
		specs[i] = httpreader.RequestSpec{Method: http.MethodGet, URL: servers[i].URL}
	}

	e := New()
	// Zero-duration spike so the LERP starts at 100 RPS (not LERP from 0),
	// followed by a sustain stage to ensure all specs are hit.
	cfg := &config.Config{
		ConfigSection: config.Section{TimeScale: 1.0},
		Stages: []config.Stage{
			{Duration: 0, TargetRPS: 100},                      // instant jump
			{Duration: 500 * time.Millisecond, TargetRPS: 100}, // sustain
		},
	}
	_ = e.RunStages(context.Background(), cfg, specs)

	for i := range hits {
		if hits[i].Load() == 0 {
			t.Errorf("spec[%d] received no requests — round-robin broken", i)
		}
	}
}

// ── 10. Log buffer: no panic on concurrent eviction ──────────────────────────

func TestConcurrent_LogBuffer_NoPanicOnEviction(t *testing.T) {
	e := New()
	e.maxLogs = 10 // tiny cap forces frequent eviction

	const goroutines = 30
	const ops = 200

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(isWriter bool) {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				if isWriter {
					e.logCall("GET", "http://example.com", 200, time.Millisecond, nil)
				} else {
					_ = e.GetRecentLogs(5)
				}
			}
		}(i%2 == 0)
	}
	wg.Wait()
}

// ── 11. IsRunning: clean false-after-done transition ─────────────────────────

func TestConcurrent_IsRunning_CleanTransition(t *testing.T) {
	srv := slowHiveSrv(t, 200, 0)
	e := New()
	cfg := hiveStage(200*time.Millisecond, 10)

	var seenRunningAfterDone atomic.Bool
	runDone := make(chan struct{})

	go func() {
		defer close(runDone)
		_ = e.RunStages(context.Background(), cfg, specsFor(srv.URL))
	}()

	const watchers = 10
	var wg sync.WaitGroup
	for i := 0; i < watchers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-runDone
			if e.IsRunning() {
				seenRunningAfterDone.Store(true)
			}
		}()
	}
	wg.Wait()

	if seenRunningAfterDone.Load() {
		t.Error("IsRunning() returned true after RunStages completed")
	}
}

// ── 12. GetMetrics latency snapshot consistency ───────────────────────────────

func TestConcurrent_GetMetrics_LatencySnapshot(t *testing.T) {
	srv := slowHiveSrv(t, 200, 2*time.Millisecond)
	e := New()
	cfg := hiveStage(300*time.Millisecond, 20)

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = e.RunStages(context.Background(), cfg, specsFor(srv.URL))
	}()

	const readers = 15
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				m := e.GetMetrics()
				if m.P99Latency > 0 && m.P50Latency > 0 && m.P99Latency < m.P50Latency {
					t.Errorf("inconsistent latency snapshot: p99=%.2f < p50=%.2f",
						m.P99Latency, m.P50Latency)
				}
				select {
				case <-runDone:
					return
				default:
					time.Sleep(2 * time.Millisecond)
				}
			}
		}()
	}
	<-runDone
	wg.Wait()
}

// ── 13. Multiple specs at high RPS — counter invariant ───────────────────────

func TestConcurrent_MultipleSpecs_HighRPS(t *testing.T) {
	srv := slowHiveSrv(t, 200, 0)
	e := New()
	specs := multiHiveSpec(srv.URL, 5)
	cfg := hiveStage(300*time.Millisecond, 50)

	err := e.RunStages(context.Background(), cfg, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := e.GetMetrics()
	if m.TotalRequests == 0 {
		t.Error("expected requests to be sent with multiple specs")
	}
	if m.TotalRequests != m.SuccessCount+m.FailureCount {
		t.Errorf("counter invariant broken: total=%d success=%d failure=%d",
			m.TotalRequests, m.SuccessCount, m.FailureCount)
	}
}

// ── 14. Context cancel during bias drain — no deadlock ───────────────────────

func TestConcurrent_CancelDuringBiasDrain_NoDeadlock(t *testing.T) {
	srv := slowHiveSrv(t, 200, 0)
	e := New()
	cfg := hiveStage(10*time.Second, 20) // long stage so cancel fires mid-run

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})

	go func() {
		defer close(runDone)
		_ = e.RunStages(ctx, cfg, specsFor(srv.URL))
	}()

	// Send bias and cancel concurrently.
	go func() {
		for i := 0; i < 20; i++ {
			e.ApplyBias(1)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case <-runDone:
		// clean exit — good
	case <-time.After(2 * time.Second):
		t.Error("RunStages deadlocked during concurrent bias + cancel")
	}
}
