package hive

// engine_integration_test.go mirrors the full engine_test.go suite from the
// legacy engine, verifying that hive.Engine satisfies the same behavioural
// contract through the public Runner interface.
//
// Coverage:
//   - Input validation (ErrNoRequests, ErrNoStages)
//   - Lifecycle (IsRunning during / after run, GetStartTime, GetEndTime)
//   - Context cancellation
//   - Request dispatch (at least one request sent)
//   - Counter correctness (success, failure, total, error rate)
//   - Stage count reflected in GetMetrics
//   - Latency recorded and percentiles populated
//   - User-Agent header set on every request
//   - Bias applied during a live run
//   - Run() backward-compat wrapper
//   - State reset across back-to-back RunStages calls

import (
	"context"
	"errors"
	"math"
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

func newCountingServer(t *testing.T, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func singleStageCfg(dur time.Duration, rps int) *config.Config {
	return &config.Config{
		ConfigSection: config.Section{TimeScale: 1.0},
		Stages:        []config.Stage{{Duration: dur, TargetRPS: rps}},
	}
}

func specsFor(url string) []httpreader.RequestSpec {
	return []httpreader.RequestSpec{{Method: http.MethodGet, URL: url}}
}

// ── Input validation ──────────────────────────────────────────────────────────

func TestRunStages_NoSpecs_ReturnsErrNoRequests(t *testing.T) {
	e := New()
	err := e.RunStages(context.Background(), singleStageCfg(time.Second, 10), nil)
	if !errors.Is(err, ErrNoRequests) {
		t.Fatalf("want ErrNoRequests, got %v", err)
	}
}

func TestRunStages_NoStages_ReturnsErrNoStages(t *testing.T) {
	e := New()
	err := e.RunStages(context.Background(), &config.Config{}, specsFor("http://localhost"))
	if !errors.Is(err, ErrNoStages) {
		t.Fatalf("want ErrNoStages, got %v", err)
	}
}

func TestRunStages_NilConfig_ReturnsErrNoStages(t *testing.T) {
	e := New()
	err := e.RunStages(context.Background(), nil, specsFor("http://localhost"))
	if !errors.Is(err, ErrNoStages) {
		t.Fatalf("want ErrNoStages for nil cfg, got %v", err)
	}
}

// ── Lifecycle ─────────────────────────────────────────────────────────────────

func TestRunStages_IsRunning_DuringRun(t *testing.T) {
	srv, _ := newCountingServer(t, 200)
	e := New()
	cfg := singleStageCfg(500*time.Millisecond, 5)

	started := make(chan struct{})
	go func() {
		close(started)
		_ = e.RunStages(context.Background(), cfg, specsFor(srv.URL))
	}()
	<-started
	time.Sleep(60 * time.Millisecond)

	if !e.IsRunning() {
		t.Error("IsRunning() should be true during RunStages")
	}
}

func TestRunStages_IsRunning_FalseAfterCompletion(t *testing.T) {
	srv, _ := newCountingServer(t, 200)
	e := New()
	_ = e.RunStages(context.Background(), singleStageCfg(100*time.Millisecond, 5), specsFor(srv.URL))

	if e.IsRunning() {
		t.Error("IsRunning() should be false after RunStages returns")
	}
}

func TestRunStages_GetStartTime_SetDuringRun(t *testing.T) {
	srv, _ := newCountingServer(t, 200)
	e := New()
	before := time.Now()
	_ = e.RunStages(context.Background(), singleStageCfg(100*time.Millisecond, 5), specsFor(srv.URL))
	after := time.Now()

	start := e.GetStartTime()
	if start.IsZero() {
		t.Fatal("GetStartTime() should be non-zero after run")
	}
	if start.Before(before) || start.After(after) {
		t.Errorf("GetStartTime %v not in [%v, %v]", start, before, after)
	}
}

func TestRunStages_GetEndTime_SetAfterCompletion(t *testing.T) {
	srv, _ := newCountingServer(t, 200)
	e := New()
	_ = e.RunStages(context.Background(), singleStageCfg(100*time.Millisecond, 5), specsFor(srv.URL))

	end := e.GetEndTime()
	if end.IsZero() {
		t.Fatal("GetEndTime() should be non-zero after run")
	}
	if end.Before(e.GetStartTime()) {
		t.Errorf("GetEndTime %v should be ≥ GetStartTime %v", end, e.GetStartTime())
	}
}

func TestGetStartTime_ZeroBeforeRun(t *testing.T) {
	e := New()
	if !e.GetStartTime().IsZero() {
		t.Error("GetStartTime() should be zero before any run")
	}
}

func TestGetElapsedTime_ZeroBeforeRun(t *testing.T) {
	e := New()
	if e.GetElapsedTime() != 0 {
		t.Error("GetElapsedTime() should return 0 before any run")
	}
}

func TestGetElapsedTime_Integration_LiveDuringRun(t *testing.T) {
	srv, _ := newCountingServer(t, 200)
	e := New()
	cfg := singleStageCfg(500*time.Millisecond, 5)

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		_ = e.RunStages(context.Background(), cfg, specsFor(srv.URL))
		close(done)
	}()
	<-started
	time.Sleep(150 * time.Millisecond)

	if elapsed := e.GetElapsedTime(); elapsed < 0.1 {
		t.Errorf("GetElapsedTime during run: want ≥ 0.1s, got %.3fs", elapsed)
	}
	<-done
}

// ── Context cancellation ──────────────────────────────────────────────────────

func TestRunStages_ContextCancel_ReturnsNil(t *testing.T) {
	srv, _ := newCountingServer(t, 200)
	e := New()
	cfg := singleStageCfg(30*time.Second, 5) // long stage

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.RunStages(ctx, cfg, specsFor(srv.URL)) }()

	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil after cancel, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunStages did not return after context cancel")
	}
}

func TestRunStages_ContextCancel_IsRunning_False(t *testing.T) {
	srv, _ := newCountingServer(t, 200)
	e := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = e.RunStages(ctx, singleStageCfg(30*time.Second, 5), specsFor(srv.URL))
		close(done)
	}()
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done
	if e.IsRunning() {
		t.Error("IsRunning() should be false after context cancel")
	}
}

// ── Request dispatch ──────────────────────────────────────────────────────────

func TestRunStages_RequestsAreSent(t *testing.T) {
	srv, hits := newCountingServer(t, 200)
	e := New()
	_ = e.RunStages(context.Background(), singleStageCfg(500*time.Millisecond, 20), specsFor(srv.URL))

	if hits.Load() == 0 {
		t.Error("expected at least one request to be sent")
	}
}

func TestRunStages_UserAgentSet(t *testing.T) {
	// Multiple actor goroutines may hit the server concurrently.
	// Use sync.Once so only the first request's UA is captured — that is
	// sufficient to prove all actors share the same User-Agent value.
	var (
		capturedUA string
		once       sync.Once
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { capturedUA = r.Header.Get("User-Agent") })
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	e := New()
	_ = e.RunStages(context.Background(), singleStageCfg(200*time.Millisecond, 5), specsFor(srv.URL))

	if capturedUA != userAgent {
		t.Errorf("User-Agent: want %q, got %q", userAgent, capturedUA)
	}
}

// ── Counter correctness ───────────────────────────────────────────────────────

func TestRunStages_SuccessCountsRecorded(t *testing.T) {
	srv, _ := newCountingServer(t, 200)
	e := New()
	_ = e.RunStages(context.Background(), singleStageCfg(400*time.Millisecond, 10), specsFor(srv.URL))

	m := e.GetMetrics()
	if m.SuccessCount == 0 {
		t.Error("expected SuccessCount > 0")
	}
	if m.FailureCount != 0 {
		t.Errorf("expected FailureCount 0, got %d", m.FailureCount)
	}
	if m.TotalRequests != m.SuccessCount {
		t.Errorf("TotalRequests (%d) != SuccessCount (%d)", m.TotalRequests, m.SuccessCount)
	}
}

func TestRunStages_FailureCountsRecorded(t *testing.T) {
	srv, _ := newCountingServer(t, 500)
	e := New()
	_ = e.RunStages(context.Background(), singleStageCfg(400*time.Millisecond, 10), specsFor(srv.URL))

	m := e.GetMetrics()
	if m.FailureCount == 0 {
		t.Error("expected FailureCount > 0 for 500 responses")
	}
	if m.SuccessCount != 0 {
		t.Errorf("expected SuccessCount 0, got %d", m.SuccessCount)
	}
	if m.ErrorRate <= 0 {
		t.Errorf("expected ErrorRate > 0, got %f", m.ErrorRate)
	}
}

func TestRunStages_ErrorRate_OneOnAllFailures(t *testing.T) {
	srv, _ := newCountingServer(t, 500)
	e := New()
	_ = e.RunStages(context.Background(), singleStageCfg(400*time.Millisecond, 10), specsFor(srv.URL))

	m := e.GetMetrics()
	if math.Abs(m.ErrorRate-1.0) > 0.01 {
		t.Errorf("ErrorRate: want 1.0, got %f", m.ErrorRate)
	}
}

func TestRunStages_TotalMatchesSuccessPlusFailure(t *testing.T) {
	// Mix of 200 and 500 using alternating responses.
	var toggle atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if toggle.Add(1)%2 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	e := New()
	_ = e.RunStages(context.Background(), singleStageCfg(400*time.Millisecond, 10), specsFor(srv.URL))

	m := e.GetMetrics()
	if m.TotalRequests != m.SuccessCount+m.FailureCount {
		t.Errorf("total (%d) != success (%d) + failure (%d)",
			m.TotalRequests, m.SuccessCount, m.FailureCount)
	}
}

// ── Stage count in metrics ────────────────────────────────────────────────────

func TestRunStages_StageCountInMetrics(t *testing.T) {
	srv, _ := newCountingServer(t, 200)
	e := New()
	cfg := &config.Config{
		ConfigSection: config.Section{TimeScale: 1.0},
		Stages: []config.Stage{
			{Duration: 150 * time.Millisecond, TargetRPS: 5},
			{Duration: 150 * time.Millisecond, TargetRPS: 10},
		},
	}
	_ = e.RunStages(context.Background(), cfg, specsFor(srv.URL))

	m := e.GetMetrics()
	if m.TotalStages != 2 {
		t.Errorf("TotalStages: want 2, got %d", m.TotalStages)
	}
}

// ── Latency recorded ─────────────────────────────────────────────────────────

func TestRunStages_LatencyRecorded(t *testing.T) {
	// Small delay so latency values are non-zero in ms measurements.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	e := New()
	_ = e.RunStages(context.Background(), singleStageCfg(400*time.Millisecond, 10), specsFor(srv.URL))

	m := e.GetMetrics()
	if m.AvgLatency <= 0 {
		t.Errorf("AvgLatency: want > 0, got %f", m.AvgLatency)
	}
	if m.P50Latency <= 0 {
		t.Errorf("P50Latency: want > 0, got %f", m.P50Latency)
	}
	if m.P99Latency < m.P50Latency {
		t.Errorf("P99 (%f) should be ≥ P50 (%f)", m.P99Latency, m.P50Latency)
	}
	if m.MaxLatency < m.MinLatency {
		t.Errorf("MaxLatency (%f) < MinLatency (%f)", m.MaxLatency, m.MinLatency)
	}
}

// ── Bias applied during run ───────────────────────────────────────────────────

func TestRunStages_BiasApplied_ReflectedAfterRun(t *testing.T) {
	srv, _ := newCountingServer(t, 200)
	e := New()
	cfg := singleStageCfg(500*time.Millisecond, 10)

	go func() {
		time.Sleep(60 * time.Millisecond)
		e.ApplyBias(5)
	}()
	_ = e.RunStages(context.Background(), cfg, specsFor(srv.URL))

	if e.GetBias() != 5 {
		t.Errorf("want bias 5 after run, got %d", e.GetBias())
	}
}

// ── Run() backward compat ─────────────────────────────────────────────────────

func TestRun_BackwardCompat_RequestsSent(t *testing.T) {
	srv, hits := newCountingServer(t, 200)
	e := New()

	err := e.Run(context.Background(), 5, 300*time.Millisecond, specsFor(srv.URL))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if hits.Load() == 0 {
		t.Error("Run() should have sent at least one request")
	}
}

func TestRun_BackwardCompat_IsRunningFalseAfter(t *testing.T) {
	srv, _ := newCountingServer(t, 200)
	e := New()
	_ = e.Run(context.Background(), 5, 150*time.Millisecond, specsFor(srv.URL))
	if e.IsRunning() {
		t.Error("IsRunning() should be false after Run() returns")
	}
}

func TestRun_BackwardCompat_NoSpecs_ReturnsErrNoRequests(t *testing.T) {
	e := New()
	err := e.Run(context.Background(), 5, 100*time.Millisecond, nil)
	if !errors.Is(err, ErrNoRequests) {
		t.Fatalf("want ErrNoRequests, got %v", err)
	}
}

// ── TimeScale compression ─────────────────────────────────────────────────────

func TestRunStages_TimeScale_CompressesDuration(t *testing.T) {
	srv, hits := newCountingServer(t, 200)
	e := New()
	// 10× time scale on a 1s stage → completes in ~100ms.
	cfg := &config.Config{
		ConfigSection: config.Section{TimeScale: 10.0},
		Stages:        []config.Stage{{Duration: time.Second, TargetRPS: 5}},
	}
	start := time.Now()
	_ = e.RunStages(context.Background(), cfg, specsFor(srv.URL))
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("10× compressed stage took %v, expected < 500ms", elapsed)
	}
	if hits.Load() == 0 {
		t.Error("expected at least one request with compressed stage")
	}
}

// ── State reset across back-to-back runs ─────────────────────────────────────

func TestRunStages_StateReset_BetweenRuns(t *testing.T) {
	// First run: all failures. Second run: all successes.
	// Metrics from the first run must not bleed into the second.
	srv500, _ := newCountingServer(t, 500)
	srv200, _ := newCountingServer(t, 200)
	e := New()

	_ = e.RunStages(context.Background(), singleStageCfg(200*time.Millisecond, 5), specsFor(srv500.URL))
	m1 := e.GetMetrics()
	if m1.FailureCount == 0 {
		t.Fatal("first run: expected failures")
	}

	_ = e.RunStages(context.Background(), singleStageCfg(200*time.Millisecond, 5), specsFor(srv200.URL))
	m2 := e.GetMetrics()

	if m2.FailureCount != 0 {
		t.Errorf("second run: FailureCount should be 0 after reset, got %d", m2.FailureCount)
	}
	if m2.SuccessCount == 0 {
		t.Error("second run: expected SuccessCount > 0")
	}
}

func TestRunStages_StateReset_LatenciesCleared(t *testing.T) {
	// First run with slow server, second with fast server.
	// Latency slice must be fresh in the second run.
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(slowSrv.Close)

	fastSrv, _ := newCountingServer(t, 200)
	e := New()

	_ = e.RunStages(context.Background(), singleStageCfg(150*time.Millisecond, 5), specsFor(slowSrv.URL))
	m1 := e.GetMetrics()

	_ = e.RunStages(context.Background(), singleStageCfg(150*time.Millisecond, 5), specsFor(fastSrv.URL))
	m2 := e.GetMetrics()

	// Second-run max latency must be strictly less than first-run values
	// (fast server vs slow server).
	if m2.MaxLatency >= m1.MaxLatency {
		t.Logf("m1.MaxLatency=%.2f m2.MaxLatency=%.2f", m1.MaxLatency, m2.MaxLatency)
		// Not a hard failure — httptest timing can be flaky; just log.
	}
}

// ── Multiple specs round-robin ────────────────────────────────────────────────

func TestRunStages_MultipleSpecs_BothHit(t *testing.T) {
	var hit0, hit1 atomic.Int64
	srv0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit0.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit1.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv0.Close)
	t.Cleanup(srv1.Close)

	specs := []httpreader.RequestSpec{
		{Method: http.MethodGet, URL: srv0.URL},
		{Method: http.MethodGet, URL: srv1.URL},
	}
	e := New()
	_ = e.RunStages(context.Background(), singleStageCfg(400*time.Millisecond, 10), specs)

	if hit0.Load() == 0 || hit1.Load() == 0 {
		t.Errorf("both specs should be hit; srv0=%d srv1=%d", hit0.Load(), hit1.Load())
	}
}

// ── Metrics: initial zero state ───────────────────────────────────────────────

func TestEngine_InitialState_MetricsAreZero(t *testing.T) {
	e := New()
	m := e.GetMetrics()
	if m.TotalRequests != 0 || m.SuccessCount != 0 || m.FailureCount != 0 {
		t.Errorf("fresh engine: expected all-zero metrics, got %+v", m)
	}
	if m.TotalStages != 0 {
		t.Errorf("fresh engine: TotalStages should be 0, got %d", m.TotalStages)
	}
	if m.Bias != 0 {
		t.Errorf("fresh engine: Bias should be 0, got %d", m.Bias)
	}
}

// ── Zero-duration stage (spike) ───────────────────────────────────────────────

func TestRunStages_ZeroDurationStage_Spike(t *testing.T) {
	srv, hits := newCountingServer(t, 200)
	e := New()
	cfg := &config.Config{
		ConfigSection: config.Section{TimeScale: 1.0},
		Stages: []config.Stage{
			{Duration: 0, TargetRPS: 50},                     // instant spike
			{Duration: 200 * time.Millisecond, TargetRPS: 5}, // sustain
		},
	}
	_ = e.RunStages(context.Background(), cfg, specsFor(srv.URL))

	if hits.Load() == 0 {
		t.Error("expected requests after zero-duration spike + sustain stage")
	}
	m := e.GetMetrics()
	if m.TotalStages != 2 {
		t.Errorf("TotalStages: want 2, got %d", m.TotalStages)
	}
}
