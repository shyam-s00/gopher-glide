package hive

import "github.com/shyam-s00/gopher-glide/internal/engine"

// GetMetrics returns a point-in-time snapshot of all engine counters,
// latency percentiles, stage progress, and director-mode bias.
//
// Reading across the 16 sharded counter arrays is not collectively atomic.
// A snapshot taken during a live run may transiently show success+failure off
// by 1 vs. totalRequests — this is mathematically acceptable for a ~10 Hz TUI
// and correct over any observation window longer than a few microseconds.
//
// Safe to call from any goroutine at any time.
func (e *Engine) GetMetrics() *engine.MetricsSnapshot {
	// ── Counters (sum all 16 shards) ──────────────────────────────────────
	// Read success and failure BEFORE total.
	// Writers always call incTotalRequests before incSuccess/incFailure, so
	// any success/failure visible at read-time has a corresponding total
	// increment that will be captured when total is read a moment later.
	// This guarantees success+failure ≤ total in every snapshot.
	success := e.counters.loadSuccessCount()
	failed := e.counters.loadFailureCount()
	total := e.counters.loadTotalRequests()
	totalLatency := e.counters.loadTotalLatency()

	// ── Derived scalars ───────────────────────────────────────────────────
	var avgLatency float64
	if total > 0 {
		avgLatency = float64(totalLatency) / float64(total)
	}

	var errorRate float64
	if total > 0 {
		errorRate = float64(failed) / float64(total)
	}

	// ── Throughput (sliding-window RPS) ───────────────────────────────────
	throughput := e.rpsWin.rate()

	// ── Latency percentiles ───────────────────────────────────────────────
	minL, maxL, p50, p95, p99 := e.computeLatency()

	return &engine.MetricsSnapshot{
		TotalRequests: total,
		SuccessCount:  success,
		FailureCount:  failed,
		AvgLatency:    avgLatency,
		ErrorRate:     errorRate,
		Throughput:    throughput,
		ActiveVPUs:    int(e.activeActors.Load()),
		TargetRPS:     int(e.targetRPS.Load()),
		MinLatency:    minL,
		MaxLatency:    maxL,
		P50Latency:    p50,
		P95Latency:    p95,
		P99Latency:    p99,
		CurrentVPUs:   0, // reserved for a future pool-size concept
		CurrentStage:  int(e.currentStage.Load()),
		TotalStages:   int(e.totalStages.Load()),
		Bias:          int(e.rpsBias.Load()),
	}
}
