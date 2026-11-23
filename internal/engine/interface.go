package engine

import (
	"context"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// MetricsSnapshot is a point-in-time read of all engine counters and latency
// percentiles. It is the shared output type returned by Runner.GetMetrics()
// and consumed by the TUI, headless renderer, and CLI.
type MetricsSnapshot struct {
	TotalRequests int64
	SuccessCount  int64
	FailureCount  int64
	DroppedCount  int64
	AvgLatency    float64
	MinLatency    float64
	MaxLatency    float64
	P50Latency    float64
	P95Latency    float64
	P99Latency    float64
	CurrentVPUs   int
	ActiveVPUs    int
	Throughput    float64
	ErrorRate     float64
	TargetRPS     int
	// Stage progress — updated by RunStages
	CurrentStage int
	TotalStages  int
	// Director Mode
	Bias int
	// IsJourneyMode is true when any parsed Journey has more than one step.
	IsJourneyMode bool
}

// CallLog is a single recorded HTTP call (success or failure).
// It is the shared element type returned by Runner.GetRecentLogs() and
// Runner.GetRecentErrorLogs(), and consumed by the TUI log panel.
type CallLog struct {
	Timestamp  time.Time
	Method     string
	Url        string
	StatusCode int
	Duration   time.Duration
	Error      string
}

// Runner is the contract every engine implementation must satisfy: execution
// control, live observability and director-mode bias. It lives here with the
// shared types (MetricsSnapshot, CallLog) so hive imports engine, not vice versa.
type Runner interface {
	// RunStages executes all configured stages sequentially.
	// It blocks until all stages complete, the context is cancelled, or an
	// unrecoverable error occurs.
	RunStages(ctx context.Context, cfg *config.Config, specs []httpreader.RequestSpec) error

	// Run is the single-stage convenience wrapper kept for backward
	// compatibility. Implementations should delegate to RunStages internally.
	Run(ctx context.Context, targetRPS int, duration time.Duration, specs []httpreader.RequestSpec) error

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

	// GetMetrics returns a point-in-time snapshot of all engine counters and
	// latency percentiles. Safe to call from any goroutine at any time.
	GetMetrics() *MetricsSnapshot

	// GetRecentLogs returns up to count of the most recent call log entries
	// (both successes and failures).
	GetRecentLogs(count int) []CallLog

	// GetRecentErrorLogs returns up to count of the most recent call log
	// entries that resulted in an HTTP error or transport failure.
	GetRecentErrorLogs(count int) []CallLog

	// ApplyBias sends a cumulative RPS delta (e.g. +5 or -5) to the running
	// stage. Non-blocking; safe to call from the TUI event loop.
	ApplyBias(delta int)

	// GetBias returns the current cumulative manual RPS bias value.
	GetBias() int

	// SetTargetRPS allows a live override of the target RPS.
	// Useful for direct-control scenarios outside of staged profiles.
	SetTargetRPS(rps int)
}

// CodeCount is one HTTP status code and how many recent errors carried it.
type CodeCount struct {
	Code int `json:"code"`
	N    int `json:"n"`
}

// ErrorMix summarises the failures in a recent time window by kind. It is
// derived from the engine's bounded error log, so counts are a lower bound
// once Capped is true; the proportions stay meaningful.
type ErrorMix struct {
	WindowS   float64     `json:"window_s"`
	Sampled   int         `json:"sampled"`
	Transport int         `json:"transport"`
	HTTP4xx   int         `json:"http_4xx"`
	HTTP5xx   int         `json:"http_5xx"`
	Extract   int         `json:"extract"`
	TopCodes  []CodeCount `json:"top_codes,omitempty"`
	Capped    bool        `json:"capped"`

	// TopEndpoints are the most-failing endpoints in the window, grouped by
	// request spec template rather than resolved URL.
	TopEndpoints []EndpointError `json:"top_endpoints,omitempty"`
}

// EndpointError is the failure count for one endpoint in an ErrorMix window.
type EndpointError struct {
	Endpoint string `json:"endpoint"`       // "METHOD /path/{{var}}"
	N        int    `json:"n"`              // failures in the window
	Code     int    `json:"code,omitempty"` // dominant HTTP status; 0 when Kind is transport or extract
	Kind     string `json:"kind"`           // dominant failure kind: transport | http_4xx | http_5xx | extract
}

// ErrorMixer is implemented by engines that can report a recent error mix.
// It is separate from Runner so existing implementations and fakes are unaffected.
type ErrorMixer interface {
	// RecentErrorMix reports failures newer than window. ok is false when
	// there were none.
	RecentErrorMix(window time.Duration) (mix ErrorMix, ok bool)
}

// HealthWindow is request health over a trailing window, so a decision can see
// the effect of a recent change instead of the whole run's history.
type HealthWindow struct {
	Seconds   float64 `json:"seconds"`          // span actually covered
	P99Ms     float64 `json:"p99_ms"`           // over latency samples written in the window
	ErrorRate float64 `json:"error_rate"`       // failed / completed in the window
	Samples   int     `json:"samples"`          // latency samples behind P99Ms
	Capped    bool    `json:"capped,omitempty"` // the window held more samples than were read
}

// WindowedHealth is implemented by engines that can report health over the
// last d. ok is false until the engine has taken a reading.
type WindowedHealth interface {
	HealthSince(d time.Duration) (w HealthWindow, ok bool)
}

// Holder is implemented by engines that can pin the request rate, overriding
// the stage schedule and bias until released.
type Holder interface {
	// HoldRPS pins the target rate (rps >= 1); it takes effect on the next scheduling window.
	HoldRPS(rps int)
	// ReleaseHold returns to the stage schedule plus bias.
	ReleaseHold()
	// GetHoldRPS returns the pinned rate, or 0 when no hold is active.
	GetHoldRPS() int
}
