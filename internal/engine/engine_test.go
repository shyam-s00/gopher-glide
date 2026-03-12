package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newTestServer returns an httptest.Server and a pointer to a hit counter.
// statusCode controls what the server responds with.
func newTestServer(t *testing.T, statusCode int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(statusCode)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func singleStageConfig(duration time.Duration, targetRPS int) *config.Config {
	return &config.Config{
		ConfigSection: config.Section{TimeScale: 1.0},
		Stages:        []config.Stage{{Duration: duration, TargetRPS: targetRPS}},
	}
}

func specFor(url string) []httpreader.RequestSpec {
	return []httpreader.RequestSpec{{Method: "GET", URL: url}}
}

// ── percentile ────────────────────────────────────────────────────────────────

func TestPercentile_Empty(t *testing.T) {
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("want 0, got %f", got)
	}
}

func TestPercentile_SingleElement(t *testing.T) {
	if got := percentile([]float64{42}, 99); got != 42 {
		t.Errorf("want 42, got %f", got)
	}
}

func TestPercentile_Table(t *testing.T) {
	data := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		p    float64
		want float64
	}{
		{0, 1},
		{50, 5.5},
		{90, 9.1},
		{100, 10},
	}
	for _, c := range cases {
		got := percentile(data, c.p)
		if math.Abs(got-c.want) > 0.2 {
			t.Errorf("percentile(%v, %.0f): want %.2f, got %.2f", data, c.p, c.want, got)
		}
	}
}

// ── rpsWindow ─────────────────────────────────────────────────────────────────

func TestRpsWindow_ZeroBeforeRecord(t *testing.T) {
	var w rpsWindow
	if r := w.rate(); r != 0 {
		t.Errorf("want 0 before any records, got %f", r)
	}
}

func TestRpsWindow_RecordAndRate(t *testing.T) {
	var w rpsWindow
	// Back-fill the previous second's bucket manually so rate() sees a full second.
	prev := time.Now().Unix() - 1
	slot := int(prev % rpsWindowSize)
	w.seconds[slot] = prev
	w.buckets[slot] = 50

	r := w.rate()
	if r <= 0 {
		t.Errorf("want rate > 0 after recording, got %f", r)
	}
}

func TestRpsWindow_ClearsStaleSlot(t *testing.T) {
	var w rpsWindow
	// Put a value in a slot from a long time ago — should not appear in rate.
	old := time.Now().Unix() - 100
	slot := int(old % rpsWindowSize)
	w.seconds[slot] = old
	w.buckets[slot] = 999

	if r := w.rate(); r != 0 {
		t.Errorf("stale bucket should not affect rate, got %f", r)
	}
}

// ── New / initial state ───────────────────────────────────────────────────────

func TestNew_InitialState(t *testing.T) {
	e := New()

	if e.IsRunning() {
		t.Error("engine should not be running after New()")
	}
	if e.GetBias() != 0 {
		t.Errorf("bias should be 0, got %d", e.GetBias())
	}
	if e.GetElapsedTime() != 0 {
		t.Errorf("elapsed should be 0, got %f", e.GetElapsedTime())
	}

	m := e.GetMetrics()
	if m.TotalRequests != 0 {
		t.Errorf("TotalRequests should be 0, got %d", m.TotalRequests)
	}
	if m.SuccessCount != 0 {
		t.Errorf("SuccessCount should be 0, got %d", m.SuccessCount)
	}
	if m.FailureCount != 0 {
		t.Errorf("FailureCount should be 0, got %d", m.FailureCount)
	}
	if m.ErrorRate != 0 {
		t.Errorf("ErrorRate should be 0, got %f", m.ErrorRate)
	}
	if m.Bias != 0 {
		t.Errorf("Bias should be 0, got %d", m.Bias)
	}
}

// ── ApplyBias / GetBias ───────────────────────────────────────────────────────

func TestApplyBias_Accumulates(t *testing.T) {
	e := New()
	// biasCh is drained by the stage runner; drain it manually here so
	// rpsBias gets updated by calling ApplyBias and then draining the channel.
	e.ApplyBias(10)
	e.ApplyBias(5)
	// Drain channel into rpsBias directly (as stage runner would)
	for {
		select {
		case d := <-e.biasCh:
			e.rpsBias.Add(int64(d))
		default:
			goto done
		}
	}
done:
	if got := e.GetBias(); got != 15 {
		t.Errorf("want bias 15, got %d", got)
	}
}

func TestApplyBias_Negative(t *testing.T) {
	e := New()
	e.ApplyBias(-5)
	for {
		select {
		case d := <-e.biasCh:
			e.rpsBias.Add(int64(d))
		default:
			goto done
		}
	}
done:
	if got := e.GetBias(); got != -5 {
		t.Errorf("want bias -5, got %d", got)
	}
}

func TestApplyBias_ReflectedInMetrics(t *testing.T) {
	e := New()
	e.rpsBias.Store(20)
	m := e.GetMetrics()
	if m.Bias != 20 {
		t.Errorf("GetMetrics().Bias: want 20, got %d", m.Bias)
	}
}

// ── logCall / GetRecentLogs / GetRecentErrorLogs ──────────────────────────────

func TestLogCall_Success(t *testing.T) {
	e := New()
	e.logCall("GET", "http://example.com", 200, 10*time.Millisecond, nil)

	logs := e.GetRecentLogs(10)
	if len(logs) != 1 {
		t.Fatalf("want 1 log, got %d", len(logs))
	}
	l := logs[0]
	if l.Method != "GET" {
		t.Errorf("Method: want GET, got %s", l.Method)
	}
	if l.StatusCode != 200 {
		t.Errorf("StatusCode: want 200, got %d", l.StatusCode)
	}
	if l.Error != "" {
		t.Errorf("Error should be empty, got %q", l.Error)
	}
}

func TestLogCall_Error_GoesToErrorLogs(t *testing.T) {
	e := New()
	e.logCall("POST", "http://example.com", 500, 5*time.Millisecond, ErrHttpError)

	allLogs := e.GetRecentLogs(10)
	errLogs := e.GetRecentErrorLogs(10)

	if len(allLogs) != 1 {
		t.Errorf("want 1 in allLogs, got %d", len(allLogs))
	}
	if len(errLogs) != 1 {
		t.Errorf("want 1 in errorLogs, got %d", len(errLogs))
	}
	if errLogs[0].Error == "" {
		t.Error("errorLog.Error should be non-empty")
	}
}

func TestLogCall_4xx_GoesToErrorLogs(t *testing.T) {
	e := New()
	e.logCall("GET", "http://example.com", 404, 5*time.Millisecond, ErrHttpError)

	if len(e.GetRecentErrorLogs(10)) != 1 {
		t.Error("4xx response should appear in error logs")
	}
}

func TestLogCall_2xx_NotInErrorLogs(t *testing.T) {
	e := New()
	e.logCall("GET", "http://example.com", 201, 5*time.Millisecond, nil)

	if len(e.GetRecentErrorLogs(10)) != 0 {
		t.Error("2xx response should not appear in error logs")
	}
}

func TestLogCall_BufferEviction(t *testing.T) {
	e := New()
	e.maxLogs = 5

	for i := 0; i < 10; i++ {
		e.logCall("GET", fmt.Sprintf("http://example.com/%d", i), 200, time.Millisecond, nil)
	}

	logs := e.GetRecentLogs(10)
	if len(logs) != 5 {
		t.Errorf("buffer should hold at most 5, got %d", len(logs))
	}
	// Most recent entry should be the last one added
	if logs[len(logs)-1].Url != "http://example.com/9" {
		t.Errorf("last log should be /9, got %s", logs[len(logs)-1].Url)
	}
}

func TestGetRecentLogs_CountCap(t *testing.T) {
	e := New()
	for i := 0; i < 10; i++ {
		e.logCall("GET", "http://example.com", 200, time.Millisecond, nil)
	}
	if got := e.GetRecentLogs(3); len(got) != 3 {
		t.Errorf("want 3 logs, got %d", len(got))
	}
}

func TestGetRecentLogs_Empty(t *testing.T) {
	e := New()
	if logs := e.GetRecentLogs(10); len(logs) != 0 {
		t.Errorf("want 0 logs on fresh engine, got %d", len(logs))
	}
}

// ── GetElapsedTime ────────────────────────────────────────────────────────────

func TestGetElapsedTime_ZeroBeforeRun(t *testing.T) {
	e := New()
	if e.GetElapsedTime() != 0 {
		t.Error("elapsed should be 0 before any run")
	}
}

func TestGetElapsedTime_AfterRun(t *testing.T) {
	e := New()
	e.startTime = time.Now().Add(-2 * time.Second)
	e.endTime = time.Now()

	elapsed := e.GetElapsedTime()
	if elapsed < 1.5 || elapsed > 3.0 {
		t.Errorf("elapsed: want ~2s, got %.2fs", elapsed)
	}
}

func TestGetElapsedTime_DuringRun(t *testing.T) {
	e := New()
	e.startTime = time.Now().Add(-1 * time.Second)
	// endTime is zero — engine still running

	elapsed := e.GetElapsedTime()
	if elapsed < 0.9 {
		t.Errorf("elapsed during run: want ≥ 0.9s, got %.2fs", elapsed)
	}
}

// ── RunStages — error cases ───────────────────────────────────────────────────

func TestRunStages_NoSpecs(t *testing.T) {
	e := New()
	cfg := singleStageConfig(time.Second, 10)
	err := e.RunStages(context.Background(), cfg, nil)
	if !errors.Is(err, ErrNoRequests) {
		t.Errorf("want ErrNoRequests, got %v", err)
	}
}

func TestRunStages_NoStages(t *testing.T) {
	e := New()
	cfg := &config.Config{}
	err := e.RunStages(context.Background(), cfg, specFor("http://localhost"))
	if !errors.Is(err, ErrNoStages) {
		t.Errorf("want ErrNoStages, got %v", err)
	}
}

// ── RunStages — lifecycle ─────────────────────────────────────────────────────

func TestRunStages_IsRunning(t *testing.T) {
	srv, _ := newTestServer(t, 200)
	e := New()
	cfg := singleStageConfig(200*time.Millisecond, 5)

	started := make(chan struct{})
	go func() {
		close(started)
		_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))
	}()
	<-started
	time.Sleep(50 * time.Millisecond)

	if !e.IsRunning() {
		t.Error("engine should be running during RunStages")
	}
}

func TestRunStages_NotRunningAfterCompletion(t *testing.T) {
	srv, _ := newTestServer(t, 200)
	e := New()
	cfg := singleStageConfig(100*time.Millisecond, 5)

	_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))

	if e.IsRunning() {
		t.Error("engine should not be running after RunStages completes")
	}
}

func TestRunStages_ContextCancellation(t *testing.T) {
	srv, _ := newTestServer(t, 200)
	e := New()
	cfg := singleStageConfig(10*time.Second, 5) // long stage

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- e.RunStages(ctx, cfg, specFor(srv.URL))
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("unexpected error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("RunStages did not return after context cancel")
	}
}

// ── RunStages — request counting ─────────────────────────────────────────────

func TestRunStages_RequestsAreSent(t *testing.T) {
	srv, hits := newTestServer(t, 200)
	e := New()
	cfg := singleStageConfig(500*time.Millisecond, 20)

	_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))

	if hits.Load() == 0 {
		t.Error("expected at least one request to be sent")
	}
}

func TestRunStages_SuccessCountsRecorded(t *testing.T) {
	srv, _ := newTestServer(t, 200)
	e := New()
	cfg := singleStageConfig(300*time.Millisecond, 10)

	_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))

	m := e.GetMetrics()
	if m.SuccessCount == 0 {
		t.Error("expected SuccessCount > 0 after run")
	}
	if m.FailureCount != 0 {
		t.Errorf("expected FailureCount 0, got %d", m.FailureCount)
	}
	if m.TotalRequests != m.SuccessCount {
		t.Errorf("TotalRequests (%d) != SuccessCount (%d)", m.TotalRequests, m.SuccessCount)
	}
}

func TestRunStages_FailureCountsRecorded(t *testing.T) {
	srv, _ := newTestServer(t, 500)
	e := New()
	cfg := singleStageConfig(300*time.Millisecond, 10)

	_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))

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

func TestRunStages_ErrorRate_IsOne_OnAllFailures(t *testing.T) {
	srv, _ := newTestServer(t, 500)
	e := New()
	cfg := singleStageConfig(300*time.Millisecond, 10)

	_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))

	m := e.GetMetrics()
	if math.Abs(m.ErrorRate-1.0) > 0.01 {
		t.Errorf("ErrorRate: want 1.0, got %f", m.ErrorRate)
	}
}

// ── RunStages — metrics after run ────────────────────────────────────────────

func TestRunStages_StageCountInMetrics(t *testing.T) {
	srv, _ := newTestServer(t, 200)
	e := New()
	cfg := &config.Config{
		ConfigSection: config.Section{TimeScale: 1.0},
		Stages: []config.Stage{
			{Duration: 150 * time.Millisecond, TargetRPS: 5},
			{Duration: 150 * time.Millisecond, TargetRPS: 10},
		},
	}

	_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))

	m := e.GetMetrics()
	if m.TotalStages != 2 {
		t.Errorf("TotalStages: want 2, got %d", m.TotalStages)
	}
}

func TestRunStages_LatencyRecorded(t *testing.T) {
	// Add a small delay so latencies are non-zero when measured in milliseconds.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	e := New()
	cfg := singleStageConfig(300*time.Millisecond, 10)

	_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))

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
		t.Errorf("MaxLatency (%f) should be ≥ MinLatency (%f)", m.MaxLatency, m.MinLatency)
	}
}

func TestRunStages_UserAgentSet(t *testing.T) {
	var capturedUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	e := New()
	cfg := singleStageConfig(150*time.Millisecond, 5)
	_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))

	if capturedUA != userAgent {
		t.Errorf("User-Agent: want %q, got %q", userAgent, capturedUA)
	}
}

// ── RunStages — bias applied during run ──────────────────────────────────────

func TestRunStages_BiasApplied(t *testing.T) {
	srv, _ := newTestServer(t, 200)
	e := New()
	cfg := singleStageConfig(500*time.Millisecond, 10)

	go func() {
		time.Sleep(50 * time.Millisecond)
		e.ApplyBias(5)
	}()

	_ = e.RunStages(context.Background(), cfg, specFor(srv.URL))

	// Bias should be non-zero in metrics after the run
	if e.GetBias() != 5 {
		t.Errorf("want bias 5 after run, got %d", e.GetBias())
	}
}

// ── Run (backward compat) ─────────────────────────────────────────────────────

func TestRun_BackwardCompat(t *testing.T) {
	srv, hits := newTestServer(t, 200)
	e := New()

	err := e.Run(context.Background(), 5, 300*time.Millisecond, specFor(srv.URL))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if hits.Load() == 0 {
		t.Error("Run should have sent at least one request")
	}
}

// ── computeLatency ────────────────────────────────────────────────────────────

func TestComputeLatency_Empty(t *testing.T) {
	e := New()
	minL, maxL, p50, p95, p99 := e.computeLatency()
	if minL != 0 || maxL != 0 || p50 != 0 || p95 != 0 || p99 != 0 {
		t.Error("all latency values should be 0 when no data")
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
		t.Errorf("p50: want ~55, got %f", p50)
	}
	if p95 < 90 {
		t.Errorf("p95: want ≥ 90, got %f", p95)
	}
	if p99 < p95 {
		t.Errorf("p99 (%f) should be ≥ p95 (%f)", p99, p95)
	}
}
