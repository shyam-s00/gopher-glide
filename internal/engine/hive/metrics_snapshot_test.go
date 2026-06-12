package hive

import (
	"net/http"
	"testing"
	"time"
)

// ── zero state ────────────────────────────────────────────────────────────────

func TestGetMetrics_InitialZeroState(t *testing.T) {
	e := New()
	m := e.GetMetrics()

	if m.TotalRequests != 0 {
		t.Errorf("TotalRequests: want 0, got %d", m.TotalRequests)
	}
	if m.SuccessCount != 0 {
		t.Errorf("SuccessCount: want 0, got %d", m.SuccessCount)
	}
	if m.FailureCount != 0 {
		t.Errorf("FailureCount: want 0, got %d", m.FailureCount)
	}
	if m.AvgLatency != 0 {
		t.Errorf("AvgLatency: want 0, got %f", m.AvgLatency)
	}
	if m.ErrorRate != 0 {
		t.Errorf("ErrorRate: want 0, got %f", m.ErrorRate)
	}
	if m.Throughput != 0 {
		t.Errorf("Throughput: want 0, got %f", m.Throughput)
	}
	if m.MinLatency != 0 || m.MaxLatency != 0 || m.P50Latency != 0 || m.P95Latency != 0 || m.P99Latency != 0 {
		t.Errorf("latency fields: all want 0, got min=%f max=%f p50=%f p95=%f p99=%f",
			m.MinLatency, m.MaxLatency, m.P50Latency, m.P95Latency, m.P99Latency)
	}
	if m.ActiveVPUs != 0 {
		t.Errorf("ActiveVPUs: want 0, got %d", m.ActiveVPUs)
	}
	if m.TargetRPS != 0 {
		t.Errorf("TargetRPS: want 0, got %d", m.TargetRPS)
	}
	if m.CurrentStage != 0 {
		t.Errorf("CurrentStage: want 0, got %d", m.CurrentStage)
	}
	if m.TotalStages != 0 {
		t.Errorf("TotalStages: want 0, got %d", m.TotalStages)
	}
	if m.Bias != 0 {
		t.Errorf("Bias: want 0, got %d", m.Bias)
	}
}

// ── counter field mapping ─────────────────────────────────────────────────────

func TestGetMetrics_CounterFieldMapping(t *testing.T) {
	e := New()

	// Inject counts directly into the sharded counters.
	for i := 0; i < 7; i++ {
		e.counters.incTotalRequests(i)
		e.counters.incSuccess(i % numShards)
	}
	// 2 failures on separate shards.
	e.counters.incTotalRequests(0)
	e.counters.incTotalRequests(1)
	e.counters.incFailure(0)
	e.counters.incFailure(1)

	m := e.GetMetrics()

	if m.TotalRequests != 9 {
		t.Errorf("TotalRequests: want 9, got %d", m.TotalRequests)
	}
	if m.SuccessCount != 7 {
		t.Errorf("SuccessCount: want 7, got %d", m.SuccessCount)
	}
	if m.FailureCount != 2 {
		t.Errorf("FailureCount: want 2, got %d", m.FailureCount)
	}
}

// ── error-rate formula ────────────────────────────────────────────────────────

func TestGetMetrics_ErrorRateFormula(t *testing.T) {
	e := New()

	// 1 total, 1 failure → error rate = 1.0
	e.counters.incTotalRequests(0)
	e.counters.incFailure(0)
	m := e.GetMetrics()
	if m.ErrorRate != 1.0 {
		t.Errorf("ErrorRate: want 1.0, got %f", m.ErrorRate)
	}

	// Add 3 more totals (no failures) → 1/4 = 0.25
	for i := 1; i <= 3; i++ {
		e.counters.incTotalRequests(i)
		e.counters.incSuccess(i)
	}
	m = e.GetMetrics()
	want := 1.0 / 4.0
	if m.ErrorRate != want {
		t.Errorf("ErrorRate: want %f, got %f", want, m.ErrorRate)
	}
}

// ── average latency formula ───────────────────────────────────────────────────

func TestGetMetrics_AvgLatency(t *testing.T) {
	e := New()

	// Record 4 requests with total latency = 100 ms → avg = 25 ms.
	for i := 0; i < 4; i++ {
		e.counters.incTotalRequests(i)
		e.counters.addLatency(i, 25)
	}
	m := e.GetMetrics()
	if m.AvgLatency != 25.0 {
		t.Errorf("AvgLatency: want 25, got %f", m.AvgLatency)
	}
}

// ── latency percentile round-trip ─────────────────────────────────────────────

func TestGetMetrics_LatencyPercentiles(t *testing.T) {
	t.Skip("latency write path not yet implemented")
}

// ── stage progress fields ─────────────────────────────────────────────────────

func TestGetMetrics_StageProgress(t *testing.T) {
	e := New()
	e.currentStage.Store(2)
	e.totalStages.Store(5)

	m := e.GetMetrics()
	if m.CurrentStage != 2 {
		t.Errorf("CurrentStage: want 2, got %d", m.CurrentStage)
	}
	if m.TotalStages != 5 {
		t.Errorf("TotalStages: want 5, got %d", m.TotalStages)
	}
}

// ── director bias field ───────────────────────────────────────────────────────

func TestGetMetrics_BiasField(t *testing.T) {
	e := New()
	e.rpsBias.Store(42)

	m := e.GetMetrics()
	if m.Bias != 42 {
		t.Errorf("Bias: want 42, got %d", m.Bias)
	}
}

// ── targetRPS and activeActors ────────────────────────────────────────────────

func TestGetMetrics_TargetRPSAndActiveActors(t *testing.T) {
	e := New()
	e.targetRPS.Store(500)
	e.activeActors.Store(37)

	m := e.GetMetrics()
	if m.TargetRPS != 500 {
		t.Errorf("TargetRPS: want 500, got %d", m.TargetRPS)
	}
	if m.ActiveVPUs != 37 {
		t.Errorf("ActiveVPUs: want 37, got %d", m.ActiveVPUs)
	}
}

// ── throughput wired to rpsWin ────────────────────────────────────────────────

func TestGetMetrics_ThroughputFromRpsWindow(t *testing.T) {
	e := New()

	// Record 100 requests in the previous second slot so rate() returns > 0.
	prev := time.Now().Unix() - 1
	slot := int(prev % rpsWindowSize)
	e.rpsWin.seconds[slot].Store(prev)
	e.rpsWin.buckets[slot].Store(100)

	m := e.GetMetrics()
	if m.Throughput <= 0 {
		t.Errorf("Throughput: want > 0, got %f", m.Throughput)
	}
}

// ── zero total → no division-by-zero ─────────────────────────────────────────

func TestGetMetrics_ZeroTotalNoDivisionByZero(t *testing.T) {
	e := New()
	// No requests recorded — this must not panic.
	m := e.GetMetrics()
	if m.AvgLatency != 0 {
		t.Errorf("AvgLatency with zero total: want 0, got %f", m.AvgLatency)
	}
	if m.ErrorRate != 0 {
		t.Errorf("ErrorRate with zero total: want 0, got %f", m.ErrorRate)
	}
}

// ── logCall round-trip into GetMetrics call-log buffers ───────────────────────

func TestGetMetrics_CallLogIntegration(t *testing.T) {
	e := New()
	e.logCall(0, "GET", "http://example.com", http.StatusOK, 5*time.Millisecond, nil)
	e.logCall(0, "GET", "http://example.com", http.StatusInternalServerError, 8*time.Millisecond, nil)

	logs := e.GetRecentLogs(10)
	errs := e.GetRecentErrorLogs(10)
	if len(logs) != 2 {
		t.Errorf("GetRecentLogs: want 2, got %d", len(logs))
	}
	if len(errs) != 1 {
		t.Errorf("GetRecentErrorLogs: want 1, got %d", len(errs))
	}
}

// ── concurrent reads during counter writes ────────────────────────────────────

func TestGetMetrics_ConcurrentSafe(t *testing.T) {
	e := New()
	done := make(chan struct{})

	// 8 goroutines incrementing counters.
	for i := 0; i < 8; i++ {
		go func(shard int) {
			for j := 0; j < 100; j++ {
				e.counters.incTotalRequests(shard)
				e.counters.incSuccess(shard)
			}
			done <- struct{}{}
		}(i)
	}

	// 4 goroutines reading metrics.
	for i := 0; i < 4; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				m := e.GetMetrics()
				// success+failure must never exceed total.
				// GetMetrics reads success/failure before total so this invariant
				// is guaranteed: any success visible at read-time has its
				// corresponding totalRequests increment captured in the later read.
				if m.SuccessCount+m.FailureCount > m.TotalRequests {
					t.Errorf("invariant broken: success=%d + failure=%d > total=%d",
						m.SuccessCount, m.FailureCount, m.TotalRequests)
				}
			}
			done <- struct{}{}
		}()
	}

	for i := 0; i < 12; i++ {
		<-done
	}
}
