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
	"math"
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

const userAgent = httpreader.UserAgent

// Engine is the Hive Engine. It implements engine.Runner and is the default
// (and only) execution engine for gg — see the package doc for its internal
// architecture.
type Engine struct {
	// Shared HTTP client — all Actors borrow this, never own it.
	client *http.Client

	counters     metrics
	activeActors atomic.Int32
	rpsWin       rpsWindow

	latBuf atomic.Pointer[latencyBuf]

	// logShards is sharded across numShards independent mutexes — see calllog.go.
	logShards [numShards]logShard
	maxLogs   int

	isRunning atomic.Bool
	startTime time.Time
	endTime   time.Time
	// timeMu protects startTime and endTime. Both are written exactly once
	// (startTime at RunStages entry, endTime in its defer) but may be read
	// concurrently by the TUI at ~10 Hz via GetMetrics / GetElapsedTime.
	timeMu sync.RWMutex

	// currentStage/totalStages are written by the Queen, read by the TUI
	// (and headless renderer) via GetMetrics.
	currentStage atomic.Int32
	totalStages  atomic.Int32

	// isJourneyMode is true when any parsed Journey has more than one step.
	isJourneyMode atomic.Bool

	targetRPS atomic.Int64
	rpsBias   atomic.Int64
	// biasCh receives RPS delta values from Director Mode — the TUI's
	// arrow keys or a headless `bias` control command (LCP). Buffered so
	// neither sender ever blocks regardless of Queen drain speed.
	biasCh chan int

	recorder    snap.Recorder
	sampleCount atomic.Int64
	sampleEvery int // 0 = disabled; N = capture 1-in-N responses
}

// EngineOption is a functional option for New().
type EngineOption func(*Engine)

// New creates a Hive Engine with a default HTTP client and sane defaults.
// Functional options are applied last and can override any default.
//
// The initial transport is built with buildTransport(1000) as a conservative
// baseline. RunStages will rebuild it to match the actual peak RPS of the
// test plan before the first request is dispatched.
func New(opts ...EngineOption) *Engine {
	e := &Engine{
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: buildTransport(1000), // baseline; tuned in RunStages
		},
		maxLogs:     100,
		biasCh:      make(chan int, 16),
		sampleEvery: 20, // 5 % default — 1-in-20 responses body-sampled
	}
	for i := range e.logShards {
		e.logShards[i].callLogs = make([]engine.CallLog, e.maxLogs)
		e.logShards[i].errorLogs = make([]engine.CallLog, e.maxLogs)
	}
	initialBuf := newLatencyBuf(1024)
	e.latBuf.Store(&initialBuf)
	for _, opt := range opts {
		opt(e)
	}
	return e
}

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

	// Smart Detection: group the flat, ordered spec list into Journeys up
	// front. Requests linked by an @gg-export → {{var}} chain become a
	// single multi-step Journey sharing one ActorMemory; every other
	// request becomes its own independent single-step Journey — so a
	// purely stateless plan dispatches exactly as it did before.
	journeys := httpreader.GroupJourneys(specs)

	isJourneyMode := false
	for _, j := range journeys {
		if len(j.Specs) > 1 {
			isJourneyMode = true
			break
		}
	}
	e.isJourneyMode.Store(isJourneyMode)

	timeScale := cfg.ConfigSection.TimeScale
	if timeScale <= 0 {
		timeScale = 1.0
	}

	// Compute peakRPS and buffer capacity up front — both are needed in the
	// reset section (before isRunning is set) and later for connection pooling.
	peakRPS := cfg.PeakRPS()
	if peakRPS < 1 {
		peakRPS = 1
	}
	totalSecs := math.Ceil(cfg.TotalDuration().Seconds())
	if totalSecs < 1 {
		totalSecs = 1
	}
	// Desired capacity: peakRPS × totalSecs + 10 % headroom.
	// Hard cap at maxLatencyBufCap to prevent OOM on long / high-RPS runs.
	//
	// Example worst-case without the cap:
	//   50 000 RPS × 3 600 s × 1.1 = 198 000 000 atomic.Uint64 ≈ 1.6 GB
	//
	// With the cap we allocate at most 8 MB (1 M × 8 B). The ring buffer
	// already wraps, so only the oldest entries are evicted — percentile
	// accuracy is unaffected; 1 M recent samples are more than enough
	// for statistically sound p50/p95/p99 estimates at any RPS.
	bufCap := int(float64(peakRPS)*totalSecs*1.1 + 0.5) // +10 %, round up
	if bufCap < minLatencyBufCap {
		bufCap = minLatencyBufCap
	}
	if bufCap > maxLatencyBufCap {
		bufCap = maxLatencyBufCap
	}

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
	e.rpsWin.reset() // lock-safe zero — avoids race with concurrent GetMetrics calls
	e.targetRPS.Store(0)
	e.currentStage.Store(0)
	e.totalStages.Store(int32(len(cfg.Stages)))
	e.rpsBias.Store(0)

	// Re-allocate the latency ring buffer sized for this run.
	// cap = peakRPS × ⌈totalDurationSeconds⌉  +  10 % headroom, minimum 1024.
	// atomic.Pointer swap makes the replacement race-free: any concurrent
	// computeLatency() reader atomically loads either the old or the new pointer
	// — it can never observe a torn slice header.
	newBuf := newLatencyBuf(bufCap)
	e.latBuf.Store(&newBuf)

	for i := range e.logShards {
		s := &e.logShards[i]
		s.mu.Lock()
		s.callLogs = make([]engine.CallLog, e.maxLogs)
		s.callCount = 0
		s.errorLogs = make([]engine.CallLog, e.maxLogs)
		s.errorCount = 0
		s.mu.Unlock()
	}

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

	// Rebuild the transport before every run so pool sizes are always tuned
	// to the current test plan's peak RPS.
	//
	// Detection: the engine-owned client always has an *http.Transport (set
	// by New() via buildTransport). A caller that injected their own client
	// via WithHTTPClient will typically supply a different RoundTripper type
	// (e.g. httptest's internal transport), so the type-assertion fails and
	// we leave the custom client untouched.
	if _, ok := e.client.Transport.(*http.Transport); ok {
		e.client.Transport = buildTransport(peakRPS)
	}

	// manifestCh connects Queen to Hatchery, buffered to peak RPS so the
	// Queen never blocks on a slow Hatchery during a burst — SpawnManifests
	// are small (two ints), so the memory cost is negligible even at high RPS.
	manifestCh := make(chan SpawnManifest, peakRPS)

	g, gCtx := errgroup.WithContext(ctx)

	q := &queen{e: e}
	g.Go(func() error {
		err := q.run(gCtx, cfg.Stages, timeScale, manifestCh)
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
		return h.run(ctx, manifestCh, journeys)
	})

	err := g.Wait()

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

// Ensure *Engine satisfies engine.Runner at compile time.
var _ engine.Runner = (*Engine)(nil)
