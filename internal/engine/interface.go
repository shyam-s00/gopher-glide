package engine

import (
	"context"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// Runner is the shared contract that every engine implementation must satisfy.
//
// It covers the full capability surface used by the TUI, headless renderer,
// and CLI: execution control, live observability, and director-mode bias.
// Any new engine (e.g. internal/engine/hive) must implement this interface
// before it can be plugged in as a drop-in replacement for *Engine.
//
// Placing the interface here — alongside the shared types MetricsSnapshot,
// CallLog, etc. — avoids a separate package and keeps the dependency graph
// simple: hive imports engine for types and interface; engine never imports hive.
type Runner interface {
	// ── Execution ────────────────────────────────────────────────────────────

	// RunStages executes all configured stages sequentially.
	// It blocks until all stages complete, the context is cancelled, or an
	// unrecoverable error occurs.
	RunStages(ctx context.Context, cfg *config.Config, specs []httpreader.RequestSpec) error

	// Run is the single-stage convenience wrapper kept for backward
	// compatibility. Implementations should delegate to RunStages internally.
	Run(ctx context.Context, targetRPS int, duration time.Duration, specs []httpreader.RequestSpec) error

	// ── Lifecycle ────────────────────────────────────────────────────────────

	// IsRunning reports whether RunStages is currently active.
	IsRunning() bool

	// GetStartTime returns the wall-clock time at which the last RunStages
	// call began. Returns a zero time if the engine has not started yet.
	GetStartTime() time.Time

	// GetEndTime returns the wall-clock time at which RunStages completed.
	// Returns time.Now() if the engine has not finished yet.
	GetEndTime() time.Time

	// GetElapsedTime returns the elapsed wall-clock seconds since the last
	// RunStages call began. Returns 0 if the engine has not started yet.
	GetElapsedTime() float64

	// ── Observability ────────────────────────────────────────────────────────

	// GetMetrics returns a point-in-time snapshot of all engine counters and
	// latency percentiles. Safe to call from any goroutine at any time.
	GetMetrics() *MetricsSnapshot

	// GetRecentLogs returns up to count of the most recent call log entries
	// (both successes and failures).
	GetRecentLogs(count int) []CallLog

	// GetRecentErrorLogs returns up to count of the most recent call log
	// entries that resulted in an HTTP error or transport failure.
	GetRecentErrorLogs(count int) []CallLog

	// ── Director / bias controls ─────────────────────────────────────────────

	// ApplyBias sends a cumulative RPS delta (e.g. +5 or -5) to the running
	// stage. Non-blocking; safe to call from the TUI event loop.
	ApplyBias(delta int)

	// GetBias returns the current cumulative manual RPS bias value.
	GetBias() int

	// SetTargetRPS allows a live override of the target RPS.
	// Useful for direct-control scenarios outside of staged profiles.
	SetTargetRPS(rps int)
}

// Compile-time assertion: *Engine must satisfy Runner.
//
// If a method is ever added to Runner without a matching implementation on
// *Engine, this line will produce a clear, immediate compile error rather than
// a confusing runtime panic. No overhead — the assignment is optimised away.
var _ Runner = (*Engine)(nil)
