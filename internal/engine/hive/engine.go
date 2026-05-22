// Package hive implements the Hive Engine — an Actor Model load engine.
//
// Architecture:
//
//	RunStages → launches Queen + Hatchery under a single errgroup
//	Queen     → 1-second heartbeat ticker; emits SpawnManifests
//	Hatchery  → spaces Actor spawns evenly across each 1-second window
//	Actor     → fire-and-forget goroutine; one HTTP request then exits
package hive

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

const userAgent = "gg/1.0"

// ── Engine ────────────────────────────────────────────────────────────────────

// Engine is the Hive Engine. It implements engine.Runner and will be
// selectable via the --hive-engine flag once it reaches production parity.
//
// Internal architecture:
//
//	RunStages → launches Queen + Hatchery under a single errgroup
//	Queen     → 1-second heartbeat ticker; emits SpawnManifests
//	Hatchery  → spaces Actor spawns evenly across each 1-second window
//	Actor     → fire-and-forget goroutine; one HTTP request then exits
type Engine struct {
	// Shared HTTP client — all Actors borrow this, never own it.
	client *http.Client

	// ── Metrics ───────────────────────────────────────────────────────────
	counters     metrics
	activeActors atomic.Int32
	rpsWin       rpsWindow

	// ── Latency percentile slice ──────────────────────────────────────────
	latencies []float64
	latencyMu sync.RWMutex

	// ── Call-log ring buffers ─────────────────────────────────────────────
	callLogs   []*engine.CallLog
	errorLogs  []*engine.CallLog
	callLogsMu sync.RWMutex
	maxLogs    int

	// ── Lifecycle ────────────────────────────────────────────────────────
	isRunning atomic.Bool
	startTime time.Time
	endTime   time.Time
	// timeMu protects startTime and endTime. Both are written exactly once
	// (startTime at RunStages entry, endTime in its defer) but may be read
	// concurrently by the TUI at ~10 Hz via GetMetrics / GetElapsedTime.
	timeMu sync.RWMutex

	// ── Stage progress (written by Queen, read by TUI via GetMetrics) ────
	currentStage atomic.Int32
	totalStages  atomic.Int32

	// ── Director / bias controls ──────────────────────────────────────────
	targetRPS atomic.Int64
	rpsBias   atomic.Int64
	// biasCh receives RPS delta values from the TUI.
	// Buffered so TUI never sends a block regardless of Queen drain speed.
	biasCh chan int

	// ── Snap / sampling ───────────────────────────────────────────────────
	recorder    snap.Recorder
	sampleCount atomic.Int64
	sampleEvery int // 0 = disabled; N = capture 1-in-N responses
}

// ── Constructor ───────────────────────────────────────────────────────────────

// EngineOption is a functional option for New().
type EngineOption func(*Engine)

// New creates a Hive Engine with a default HTTP client and sane defaults.
// Functional options are applied last and can override any default.
func New(opts ...EngineOption) *Engine {
	e := &Engine{
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 100,
				MaxIdleConns:        1000,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		latencies:   make([]float64, 0, 1024),
		callLogs:    make([]*engine.CallLog, 0, 100),
		errorLogs:   make([]*engine.CallLog, 0, 100),
		maxLogs:     100,
		biasCh:      make(chan int, 16),
		sampleEvery: 20, // 5 % default — 1-in-20 responses body-sampled
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// ── engine.Runner implementation ─────────────────────────────────────────────

// RunStages executes all configured stages sequentially.
//
// Orchestration:
//  1. Validate inputs — returns ErrNoRequests or ErrNoStages immediately.
//  2. Reset all mutable state (counters, latencies, call logs, atomics).
//  3. Record startTime and set isRunning=true.
//  4. Create a buffered manifestCh (cap = peak RPS) to decouple Queen from Hatchery.
//  5. Launch Queen and Hatchery under a single errgroup so panics are caught
//     and the first non-nil error is returned to the caller.
//  6. Defer endTime recording and isRunning=false — always fires even on error.
//
// The Queen drives the simulation clock; the Hatchery converts each SpawnManifest
// into evenly-spaced Actor goroutines. RunStages blocks until both goroutines
// return (i.e. stages complete or ctx is cancelled).
func (e *Engine) RunStages(ctx context.Context, cfg *config.Config, specs []httpreader.RequestSpec) error {
	if len(specs) == 0 {
		return ErrNoRequests
	}
	if cfg == nil || len(cfg.Stages) == 0 {
		return ErrNoStages
	}

	timeScale := cfg.ConfigSection.TimeScale
	if timeScale <= 0 {
		timeScale = 1.0
	}

	// ── Reset state ───────────────────────────────────────────────────────
	// activeActors must be zero here because either:
	//   - this is the first call (always zero), or
	//   - the previous RunStages drained actors before returning.
	// We reset each shard individually via atomic stores — a value-type
	// assignment (e.counters = metrics{}) would be a non-atomic write of
	// 512 bytes that races with any stray actor goroutine still in-flight.
	for i := 0; i < numShards; i++ {
		atomic.StoreInt64(&e.counters.totalRequests[i].value, 0)
		atomic.StoreInt64(&e.counters.successCount[i].value, 0)
		atomic.StoreInt64(&e.counters.failureCount[i].value, 0)
		atomic.StoreInt64(&e.counters.totalLatency[i].value, 0)
	}
	e.activeActors.Store(0)
	e.rpsWin = rpsWindow{}
	e.targetRPS.Store(0)
	e.currentStage.Store(0)
	e.totalStages.Store(int32(len(cfg.Stages)))
	e.rpsBias.Store(0)

	e.latencyMu.Lock()
	e.latencies = make([]float64, 0, 1024)
	e.latencyMu.Unlock()

	e.callLogsMu.Lock()
	e.callLogs = make([]*engine.CallLog, 0, e.maxLogs)
	e.errorLogs = make([]*engine.CallLog, 0, e.maxLogs)
	e.callLogsMu.Unlock()

	// ── Lifecycle: mark start ─────────────────────────────────────────────
	e.timeMu.Lock()
	e.startTime = time.Now()
	e.endTime = time.Time{} // clear previous end time
	e.timeMu.Unlock()
	e.isRunning.Store(true)

	defer func() {
		e.timeMu.Lock()
		e.endTime = time.Now()
		e.timeMu.Unlock()
		e.isRunning.Store(false)
	}()

	// ── Channel connecting Queen → Hatchery ───────────────────────────────
	// Buffer sized to peak RPS so the Queen never blocks on a slow Hatchery
	// during a burst. SpawnManifests are small (two ints), so memory cost is
	// negligible even at high RPS.
	peakRPS := cfg.PeakRPS()
	if peakRPS < 1 {
		peakRPS = 1
	}
	manifestCh := make(chan SpawnManifest, peakRPS)

	// ── Launch Queen + Hatchery under errgroup ────────────────────────────
	g, gCtx := errgroup.WithContext(ctx)

	q := &queen{e: e}
	g.Go(func() error {
		err := q.run(gCtx, cfg.Stages, timeScale, specs, manifestCh)
		// Queen finished (all stages done or ctx cancelled) — close the
		// channel so the Hatchery drains remaining manifests and exits.
		close(manifestCh)
		return err
	})

	h := &hatchery{e: e}
	g.Go(func() error {
		// The Hatchery and its spawned Actors use the caller's original ctx
		// (not gCtx). This is intentional: errgroup cancels gCtx inside
		// g.Wait() even when all goroutines return nil. If Actors held gCtx,
		// their HTTP requests would be cancelled during the post-Wait drain,
		// producing spurious failure counts. Using ctx directly means:
		//   • Actors stop on user/caller cancellation (correct).
		//   • Actors do NOT stop on errgroup's internal book-keeping cancel.
		// The Hatchery exits naturally when manifestCh is closed by the Queen.
		return h.run(ctx, manifestCh, specs)
	})

	err := g.Wait()

	// ── Drain in-flight actors ────────────────────────────────────────────
	// The Hatchery's dispatch goroutine has exited, but actor goroutines it
	// spawned may still be executing their HTTP request. We wait until every
	// actor decrements activeActors (via defer) so that:
	//   a) GetMetrics counters are fully settled before RunStages returns, and
	//   b) A back-to-back RunStages call can safely reset e.counters without
	//      a data race against in-flight atomic writes from the prior run.
	//
	// Using a polling loop (not sync.WaitGroup) keeps the project philosophy
	// of "no WaitGroup". The deadline matches the HTTP client timeout, so no
	// actor can outlive it under normal conditions.
	drainDeadline := time.Now().Add(31 * time.Second)
	for e.activeActors.Load() > 0 && time.Now().Before(drainDeadline) {
		time.Sleep(time.Millisecond)
	}

	return err
}

// Run is the single-stage backward-compatibility wrapper.
//
// It builds a single-stage Config from targetRPS and duration, then delegates
// to RunStages. All existing callers that used Run() continue to work unchanged.
func (e *Engine) Run(ctx context.Context, targetRPS int, duration time.Duration, specs []httpreader.RequestSpec) error {
	cfg := &config.Config{
		ConfigSection: config.Section{TimeScale: 1.0},
		Stages:        []config.Stage{{Duration: duration, TargetRPS: targetRPS}},
	}
	return e.RunStages(ctx, cfg, specs)
}

// IsRunning, GetStartTime, GetEndTime, GetElapsedTime are implemented in lifecycle.go.

// GetMetrics is implemented in metrics_snapshot.go.

// GetRecentLogs and GetRecentErrorLogs are implemented in calllog.go.

// ApplyBias, GetBias, SetTargetRPS are implemented in director.go.

// ── Compile-time assertion ────────────────────────────────────────────────────

// Ensure *Engine satisfies engine.Runner at compile time.
var _ engine.Runner = (*Engine)(nil)
