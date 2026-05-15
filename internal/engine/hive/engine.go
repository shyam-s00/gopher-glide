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

// ── engine.Runner stubs ───────────────────────────────────────────────────────

func (e *Engine) RunStages(_ context.Context, _ *config.Config, _ []httpreader.RequestSpec) error {
	return ErrNotImplemented
}

func (e *Engine) Run(_ context.Context, _ int, _ time.Duration, _ []httpreader.RequestSpec) error {
	return ErrNotImplemented
}

func (e *Engine) IsRunning() bool                           { return false }
func (e *Engine) GetStartTime() time.Time                   { return time.Time{} }
func (e *Engine) GetEndTime() time.Time                     { return time.Now() }
func (e *Engine) GetElapsedTime() float64                   { return 0 }
func (e *Engine) GetMetrics() *engine.MetricsSnapshot       { return &engine.MetricsSnapshot{} }
func (e *Engine) GetRecentLogs(_ int) []engine.CallLog      { return nil }
func (e *Engine) GetRecentErrorLogs(_ int) []engine.CallLog { return nil }
func (e *Engine) ApplyBias(_ int)                           {}
func (e *Engine) GetBias() int                              { return 0 }
func (e *Engine) SetTargetRPS(_ int)                        {}

// ── Compile-time assertion ────────────────────────────────────────────────────

// Ensure *Engine satisfies engine.Runner at compile time.
var _ engine.Runner = (*Engine)(nil)
