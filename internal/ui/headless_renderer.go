package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// HeadlessRenderer runs the engine without any interactive TUI.
// Progress is emitted as structured heartbeat lines to stdout so that CI
// systems and log aggregators can parse or display them easily.
//
// Output format is controlled by the Reporter field:
//
//	"text" (default) – human-readable lines
//	"json" – one JSON object per heartbeat
//
// The heartbeat interval defaults to HeartbeatInterval (5 s).
type HeadlessRenderer struct {
	// Reporter selects the output format: "text" or "json".
	// Defaults to "text" when empty.
	Reporter string

	// HeartbeatInterval controls how often progress lines are emitted.
	// Defaults to 5 s when zero.
	HeartbeatInterval time.Duration
}

// heartbeatInterval returns the effective heartbeat period.
func (r *HeadlessRenderer) heartbeatInterval() time.Duration {
	if r.HeartbeatInterval > 0 {
		return r.HeartbeatInterval
	}
	return 5 * time.Second
}

// reporter returns the effective reporter name (lower-cased, default "text").
func (r *HeadlessRenderer) reporter() string {
	if norm := strings.ToLower(strings.TrimSpace(r.Reporter)); norm != "" {
		return norm
	}
	return "text"
}

// HeartbeatPayload is the structured representation of a single progress event.
// It is emitted as a JSON object when Reporter == "json", or formatted as a
// human-readable line when Reporter == "text".
type HeartbeatPayload struct {
	Time         string  `json:"time"`
	Event        string  `json:"event"` // "heartbeat" | "started" | "finished" | "snap"
	Stage        int     `json:"stage"` // 1-based
	TotalStages  int     `json:"total_stages"`
	TargetRPS    int     `json:"target_rps"`
	ActualRPS    float64 `json:"actual_rps"`
	TotalReqs    int64   `json:"total_requests"`
	SuccessCount int64   `json:"success_count"`
	FailureCount int64   `json:"failure_count"`
	ErrorRate    float64 `json:"error_rate"`
	P50Ms        float64 `json:"p50_ms"`
	P95Ms        float64 `json:"p95_ms"`
	P99Ms        float64 `json:"p99_ms"`
	Message      string  `json:"message,omitempty"` // used for snap / finish lines
}

// Run executes the engine headlessly and blocks until the run finishes or an
// interrupt signal is received. Progress heartbeats are written to stdout.
func (r *HeadlessRenderer) Run(eng *engine.Engine, cfg *config.Config, specs []httpreader.RequestSpec, opts RunOptions) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Capture SIGINT / SIGTERM so the run can be aborted cleanly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// Start the engine in a background goroutine (mirrors what tui.go does).
	engineDone := make(chan error, 1)
	go func() {
		engineDone <- eng.RunStages(ctx, cfg, specs)
	}()

	if opts.Snapping {
		r.emit(HeartbeatPayload{
			Time:    now(),
			Event:   "snap",
			Message: fmt.Sprintf("📸 Snapping → %s", opts.SnapDir),
		})
	}

	r.emit(HeartbeatPayload{
		Time:        now(),
		Event:       "started",
		TotalStages: len(cfg.Stages),
		Message:     fmt.Sprintf("Load test started — %d stage(s)", len(cfg.Stages)),
	})

	ticker := time.NewTicker(r.heartbeatInterval())
	defer ticker.Stop()

	// engineFinished tracks whether we already consumed the engineDone value
	// inside the select loop. If false after the loop we must drain the channel
	// so that no engine worker is still active when we finalize the snapshot.
	engineFinished := false
	// interrupted is set when the user/OS explicitly canceled the run via a
	// signal. It changes how we treat a non-canceled error from the engine
	// after the loop: on an explicit interrupt we log and still finalize
	// (preserving partial data); on a natural stop we propagate the error.
	interrupted := false

loop:
	for {
		select {
		case <-sigCh:
			interrupted = true
			cancel()
			r.emitMessage("interrupted", "Run interrupted by signal")
			break loop

		case err := <-engineDone:
			engineFinished = true
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("engine: %w", err)
			}
			break loop

		case <-ticker.C:
			m := eng.GetMetrics()
			if !eng.IsRunning() {
				// Engine stopped naturally between heartbeats — fall through to
				// the engineDone drain below so its return value is not lost.
				break loop
			}
			r.emit(HeartbeatPayload{
				Time:         now(),
				Event:        "heartbeat",
				Stage:        m.CurrentStage + 1,
				TotalStages:  m.TotalStages,
				TargetRPS:    m.TargetRPS,
				ActualRPS:    m.Throughput,
				TotalReqs:    m.TotalRequests,
				SuccessCount: m.SuccessCount,
				FailureCount: m.FailureCount,
				ErrorRate:    m.ErrorRate,
				P50Ms:        m.P50Latency,
				P95Ms:        m.P95Latency,
				P99Ms:        m.P99Latency,
			})
		}
	}

	// If the loop exited via the signal or ticker path (not engineDone), wait
	// here until RunStages actually returns. This guarantees that no engine
	// worker is still calling recorder.Record() when we call OnRunComplete /
	// finalizeSnapResult below — preventing a recorder data-race on early quit.
	if !engineFinished {
		if err := <-engineDone; err != nil && !errors.Is(err, context.Canceled) {
			if interrupted {
				// The run was explicitly aborted — log the unexpected error but
				// still proceed with snapshot finalization so partial data is
				// not lost entirely.
				r.emitMessage("error", fmt.Sprintf("engine exited with error: %v", err))
			} else {
				// The engine stopped naturally (detected via IsRunning) and
				// returned a real error. Propagate it so the caller sees a
				// non-zero exit; do not finalize a potentially corrupt snapshot.
				return fmt.Errorf("engine: %w", err)
			}
		}
	}

	// Run complete — call the post-run hook (e.g., write snapshot) synchronously.
	// In headless mode there is no alt-screen constraint, so printing is safe.
	if opts.OnRunComplete != nil {
		status := opts.OnRunComplete()
		if status != "" {
			r.emitMessage("finished", status)
		}
	} else {
		m := eng.GetMetrics()
		r.emit(HeartbeatPayload{
			Time:         now(),
			Event:        "finished",
			TotalReqs:    m.TotalRequests,
			SuccessCount: m.SuccessCount,
			FailureCount: m.FailureCount,
			ErrorRate:    m.ErrorRate,
			P50Ms:        m.P50Latency,
			P95Ms:        m.P95Latency,
			P99Ms:        m.P99Latency,
			Message:      "Load test completed",
		})
	}

	return nil
}

// emit writes a single HeartbeatPayload in the configured reporter format.
func (r *HeadlessRenderer) emit(p HeartbeatPayload) {
	switch r.reporter() {
	case "json":
		b, _ := json.Marshal(p)
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", b)
	default: // "text"
		switch p.Event {
		case "started", "finished", "interrupted", "snap":
			_, _ = fmt.Fprintf(os.Stdout, "[%s] %s\n", p.Time, p.Message)
		case "heartbeat":
			_, _ = fmt.Fprintf(os.Stdout,
				"[%s] stage=%d/%d  target=%d rps  actual=%.1f rps  reqs=%d  errors=%.2f%%  p50=%.1fms  p95=%.1fms  p99=%.1fms\n",
				p.Time,
				p.Stage, p.TotalStages,
				p.TargetRPS,
				p.ActualRPS,
				p.TotalReqs,
				p.ErrorRate*100,
				p.P50Ms, p.P95Ms, p.P99Ms,
			)
		default:
			if p.Message != "" {
				_, _ = fmt.Fprintf(os.Stdout, "[%s] %s\n", p.Time, p.Message)
			}
		}
	}
}

// emitMessage is a convenience helper for event lines that only carry a message.
func (r *HeadlessRenderer) emitMessage(event, message string) {
	r.emit(HeartbeatPayload{Time: now(), Event: event, Message: message})
}

// now returns the current UTC time formatted for log lines.
func now() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}
