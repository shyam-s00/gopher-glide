package engine

import (
	"context"
	"gopher-glide/internal/config"
	"gopher-glide/internal/httpreader"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// All tests in this file are designed to be run with -race.
// go test -race ./internal/engine/... -run Concurrent

// ── helpers ───────────────────────────────────────────────────────────────────

func slowServer(t *testing.T, statusCode int, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		w.WriteHeader(statusCode)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// multiSpecFor returns n identical GET specs for url.
func multiSpecFor(url string, n int) []httpreader.RequestSpec {
	specs := make([]httpreader.RequestSpec, n)
	for i := range specs {
		specs[i] = httpreader.RequestSpec{Method: "GET", URL: url}
	}
	return specs
}

// ── rpsWindow: concurrent record + rate ──────────────────────────────────────

func TestConcurrent_RpsWindow_RecordAndRate(t *testing.T) {
	var w rpsWindow
	const goroutines = 50
	const recordsEach = 200

	var wg sync.WaitGroup
	// writers
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < recordsEach; j++ {
				w.record(1)
			}
		}()
	}
	// concurrent readers — must not race with writers
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < recordsEach; j++ {
				_ = w.rate()
			}
		}()
	}
	wg.Wait()
}

// ── logCall: concurrent writes + reads ───────────────────────────────────────

func TestConcurrent_LogCall_ConcurrentWritesAndReads(t *testing.T) {
	e := New()
	const goroutines = 20
	const callsEach = 100

	var wg sync.WaitGroup
	// concurrent writers
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < callsEach; j++ {
				e.logCall("GET", "http://example.com", 200, time.Millisecond, nil)
			}
		}(i)
	}
	// concurrent readers — must not race with writers
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

	// Buffer should be capped at maxLogs regardless of total writes
	logs := e.GetRecentLogs(e.maxLogs + 100)
	if len(logs) > e.maxLogs {
		t.Errorf("callLogs exceeded maxLogs: got %d, want ≤ %d", len(logs), e.maxLogs)
	}
}

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

// ── latencies slice: concurrent appends ──────────────────────────────────────

func TestConcurrent_Latencies_ConcurrentAppends(t *testing.T) {
	e := New()
	e.latencies = make([]float64, 0, 1024)
	const goroutines = 50
	const appendsEach = 100

	var wg sync.WaitGroup
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
	// concurrent readers via computeLatency
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

	// All appends must be visible — no writes lost
	e.latencyMu.RLock()
	got := len(e.latencies)
	e.latencyMu.RUnlock()
	want := goroutines * appendsEach
	if got != want {
		t.Errorf("latencies length: want %d, got %d (writes lost under concurrency)", want, got)
	}
}

// ── GetMetrics: safe concurrent reads during a live run ──────────────────────

func TestConcurrent_GetMetrics_DuringRun(t *testing.T) {
	srv := slowServer(t, 200, 0)
	e := New()
	cfg := singleStageConfig(400*time.Millisecond, 20)

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))
	}()

	// Hammer GetMetrics from many goroutines while the engine is running.
	const readers = 20
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				m := e.GetMetrics()
				// Invariant: TotalRequests == SuccessCount + FailureCount
				if m.TotalRequests != m.SuccessCount+m.FailureCount {
					t.Errorf("counter invariant broken: total=%d success=%d failure=%d",
						m.TotalRequests, m.SuccessCount, m.FailureCount)
				}
				// ErrorRate must be in [0, 1]
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

// ── counter invariant: total == success + failure under concurrent workers ────

func TestConcurrent_CounterInvariant_MixedResponses(t *testing.T) {
	// Server alternates 200 / 500 per request.
	var reqCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reqCount.Add(1)%2 == 0 {
			w.WriteHeader(500)
		} else {
			w.WriteHeader(200)
		}
	}))
	t.Cleanup(srv.Close)

	e := New()
	cfg := singleStageConfig(400*time.Millisecond, 30)
	_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))

	m := e.GetMetrics()
	if m.TotalRequests != m.SuccessCount+m.FailureCount {
		t.Errorf("counter invariant broken after mixed run: total=%d success=%d failure=%d",
			m.TotalRequests, m.SuccessCount, m.FailureCount)
	}
	if m.TotalRequests == 0 {
		t.Error("expected requests to be sent")
	}
}

// ── activeVPU: never exceeds worker pool size ─────────────────────────────────

func TestConcurrent_ActiveVPU_NeverExceedsPoolSize(t *testing.T) {
	// Slow server so workers stay active long enough to observe concurrency.
	srv := slowServer(t, 200, 10*time.Millisecond)
	e := New()

	const targetRPS = 50
	cfg := singleStageConfig(400*time.Millisecond, targetRPS)
	peakRPS := cfg.PeakRPS()

	var maxObserved atomic.Int32
	runDone := make(chan struct{})

	go func() {
		defer close(runDone)
		_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))
	}()

	// Poll activeVPU while engine runs and track the maximum seen.
	go func() {
		for {
			select {
			case <-runDone:
				return
			default:
				cur := e.activeVPU.Load()
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

	observed := int(maxObserved.Load())
	if observed > peakRPS {
		t.Errorf("activeVPU exceeded pool size: observed %d, pool %d", observed, peakRPS)
	}
}

// ── ApplyBias: concurrent senders while stage runner is draining ──────────────

func TestConcurrent_ApplyBias_ManySenders(t *testing.T) {
	srv := slowServer(t, 200, 0)
	e := New()
	cfg := singleStageConfig(500*time.Millisecond, 10)

	const biasSenders = 10
	const biasEach = 3 // +3 per goroutine

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))
	}()

	// Fire bias updates from many goroutines while the engine runs.
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

	// Total bias absorbed must equal total sent (biasSenders * biasEach).
	// The stage runner drains biasCh into rpsBias; all 30 deltas should
	// have been consumed because the run lasted 500ms.
	got := e.GetBias()
	want := biasSenders * biasEach
	if got != want {
		t.Errorf("bias after concurrent sends: want %d, got %d", want, got)
	}
}

// ── round-robin specs: all specs receive requests ─────────────────────────────

func TestConcurrent_RoundRobin_AllSpecsReceiveRequests(t *testing.T) {
	const numSpecs = 3
	hits := make([]atomic.Int64, numSpecs)

	// Each spec points to its own handler that increments the corresponding counter.
	servers := make([]*httptest.Server, numSpecs)
	specs := make([]httpreader.RequestSpec, numSpecs)
	for i := 0; i < numSpecs; i++ {
		idx := i
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits[idx].Add(1)
			w.WriteHeader(200)
		}))
		t.Cleanup(servers[i].Close)
		specs[i] = httpreader.RequestSpec{Method: "GET", URL: servers[i].URL}
	}

	e := New()
	// Zero-duration spike to 100 RPS first so LERP starts at full rate
	// (not from 0), then sustain for 500ms. TimeScale=1 so wall time applies.
	cfg := &config.Config{
		ConfigSection: config.Section{TimeScale: 1.0},
		Stages: []config.Stage{
			{Duration: 0, TargetRPS: 100},                      // instant jump to 100 RPS
			{Duration: 500 * time.Millisecond, TargetRPS: 100}, // sustain at 100 RPS
		},
	}
	_ = e.RunStages(context.Background(), cfg, specs)

	// With 100 RPS for 500ms we expect ~50 requests, each spec getting ~16.
	// Any spec receiving zero means round-robin is broken.
	for i := range hits {
		if hits[i].Load() == 0 {
			t.Errorf("spec[%d] received no requests — round-robin broken", i)
		}
	}
}

// ── concurrent GetRecentLogs + logCall: no panic on buffer eviction ───────────

func TestConcurrent_LogBuffer_NoPanicOnEviction(t *testing.T) {
	e := New()
	e.maxLogs = 10 // tiny buffer to force frequent eviction

	var wg sync.WaitGroup
	const goroutines = 30
	const ops = 200

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

// ── IsRunning transitions: never seen as running after completion ─────────────

func TestConcurrent_IsRunning_CleanTransition(t *testing.T) {
	srv := slowServer(t, 200, 0)
	e := New()
	cfg := singleStageConfig(200*time.Millisecond, 10)

	// Poll IsRunning from many goroutines while the engine starts and stops.
	const watchers = 10
	// seenRunning and seenStopped track whether each watcher saw the expected
	// transitions — no watcher should see running=true after the run has ended.
	var seenStoppedAfterDone atomic.Bool

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))
	}()

	var wg sync.WaitGroup
	for i := 0; i < watchers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-runDone
			// After runDone is closed, IsRunning must be false.
			if e.IsRunning() {
				seenStoppedAfterDone.Store(true)
			}
		}()
	}
	wg.Wait()

	if seenStoppedAfterDone.Load() {
		t.Error("IsRunning returned true after RunStages completed")
	}
}

// ── multiple concurrent GetMetrics calls: no data race on latency snapshot ────

func TestConcurrent_GetMetrics_LatencySnapshot(t *testing.T) {
	srv := slowServer(t, 200, 2*time.Millisecond)
	e := New()
	cfg := singleStageConfig(300*time.Millisecond, 20)

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))
	}()

	const readers = 15
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				m := e.GetMetrics()
				// p99 must always be >= p50 — snapshot must be self-consistent
				if m.P99Latency > 0 && m.P50Latency > 0 && m.P99Latency < m.P50Latency {
					t.Errorf("inconsistent latency snapshot: p99=%.2f < p50=%.2f", m.P99Latency, m.P50Latency)
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

// ── multiple specs round-robin is safe under high concurrency ─────────────────

func TestConcurrent_MultipleSpecs_HighRPS(t *testing.T) {
	srv := slowServer(t, 200, 0)
	e := New()
	// Use 5 specs pointing to the same server — exercises the idx%len(specs) path.
	specs := multiSpecFor(srv.URL, 5)
	cfg := singleStageConfig(300*time.Millisecond, 50)

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

// ── context cancel races with bias drain: no deadlock or panic ───────────────

func TestConcurrent_CancelDuringBiasDrain_NoDeadlock(t *testing.T) {
	srv := slowServer(t, 200, 0)
	e := New()
	cfg := singleStageConfig(10*time.Second, 20) // long stage

	ctx, cancel := context.WithCancel(context.Background())

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = e.RunStages(ctx, cfg, specFor(srv.URL))
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
		// clean exit
	case <-time.After(2 * time.Second):
		t.Error("RunStages deadlocked during concurrent bias + cancel")
	}
}
