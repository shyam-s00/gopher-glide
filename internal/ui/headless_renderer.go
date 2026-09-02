package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// controlCmdChanCap is cmdCh's buffer size (§3.2) — generous enough for a
// burst of scripted commands or a throttled slider's queued nudges without
// blocking controlReader mid-parse.
const controlCmdChanCap = 32

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

	// ControlMode selects the stdin control protocol: "stdin" (listen for
	// bias/mark/stop commands) or "none" (read-only, matches pre-v1.3
	// behavior). Defaults to "stdin" when empty.
	ControlMode string

	// ControlInput is the source read for control commands when ControlMode
	// is "stdin". Defaults to os.Stdin; tests inject their own reader.
	ControlInput io.Reader

	// biasEvents records every bias command applied this run, for
	// SnapMeta.BiasEvents at finalize (§3.4). Exposed via BiasEvents().
	biasEvents []snap.BiasEvent

	// marks records every "mark" command received this run, for
	// SnapMeta.Marks at finalize (§3.4). Exposed via Marks().
	marks []snap.Mark
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

// controlMode returns the effective control mode (lower-cased, default
// "stdin"). main.go validates ControlMode is "stdin" or "none" before it
// reaches here.
func (r *HeadlessRenderer) controlMode() string {
	if norm := strings.ToLower(strings.TrimSpace(r.ControlMode)); norm != "" {
		return norm
	}
	return "stdin"
}

// controlInput returns ControlInput if set, else os.Stdin.
func (r *HeadlessRenderer) controlInput() io.Reader {
	if r.ControlInput != nil {
		return r.ControlInput
	}
	return os.Stdin
}

// capabilities returns the "started" event's Capabilities pointer: the full
// LCP set for "stdin", or an explicit empty slice for "none" — distinct from
// a pre-v1.3 binary that omits the field entirely (§2.4 rule 4).
func (r *HeadlessRenderer) capabilities() *[]string {
	caps := []string{}
	if r.controlMode() == "stdin" {
		caps = []string{"control.bias", "control.mark", "control.stop"}
	}
	return &caps
}

// Marks returns every "mark" command recorded this run. Safe to call once
// Run()'s select loop has broken — OnRunComplete runs synchronously before
// Run() returns, so callers see the final set (§3.5).
func (r *HeadlessRenderer) Marks() []snap.Mark {
	return r.marks
}

// BiasEvents returns every "bias" command recorded this run. Same calling
// convention as Marks.
func (r *HeadlessRenderer) BiasEvents() []snap.BiasEvent {
	return r.biasEvents
}

// protocolVersion is the LCP wire version emitted on every "started" event.
// Bumped only for breaking changes — see ignore/live-control-protocol.md §2.4.
const protocolVersion = 1

// StageInfo is a compact, JSON-friendly description of a single load stage,
// emitted on the "started" event so consumers don't need to parse the YAML
// config or profile definition themselves.
type StageInfo struct {
	Name            string  `json:"name"`
	DurationSeconds float64 `json:"duration_seconds"`
	TargetRPS       int     `json:"target_rps"`
}

// HeartbeatPayload is the structured representation of a single progress event.
// It is emitted as a JSON object when Reporter == "json", or formatted as a
// human-readable line when Reporter == "text".
type HeartbeatPayload struct {
	Time         string      `json:"time"`
	Event        string      `json:"event"` // "heartbeat" | "started" | "finished" | "interrupted" | "snap" | "error" | "ack" | "mark" | "stopped"
	Stage        int         `json:"stage"` // 1-based
	TotalStages  int         `json:"total_stages"`
	Stages       []StageInfo `json:"stages,omitempty"`        // set on "started" only
	Profile      string      `json:"profile,omitempty"`       // set on "started" only, when a --profile run
	ProfileScale float64     `json:"profile_scale,omitempty"` // set on "started" only, when a --profile run
	TargetRPS    int         `json:"target_rps"`
	ActualRPS    float64     `json:"actual_rps"`
	TotalReqs    int64       `json:"total_requests"`
	SuccessCount int64       `json:"success_count"`
	FailureCount int64       `json:"failure_count"`
	ErrorRate    float64     `json:"error_rate"`
	P50Ms        float64     `json:"p50_ms"`
	P95Ms        float64     `json:"p95_ms"`
	P99Ms        float64     `json:"p99_ms"`
	Message      string      `json:"message,omitempty"` // used for snap / finish lines

	// LCP (v1.3): control protocol fields. See ignore/live-control-protocol.md §2.6.
	ProtocolVersion int       `json:"protocol_version,omitempty"` // "started" only; 0 is never a real version
	Capabilities    *[]string `json:"capabilities,omitempty"`     // "started" only; pointer distinguishes absent from --control none's []
	Bias            int       `json:"bias"`                       // cumulative Director bias; no omitempty, mirrors other metric fields
	ID              string    `json:"id,omitempty"`               // echoes the command's caller-chosen id, if any
	Command         string    `json:"command,omitempty"`          // set on ack/error only
	Reason          string    `json:"reason,omitempty"`           // error replies only
	Label           string    `json:"label,omitempty"`            // mark replies only
	ElapsedS        float64   `json:"elapsed_s,omitempty"`        // mark replies only, run-relative offset
}

// Run executes the engine headlessly and blocks until the run finishes or an
// interrupt signal is received. Progress heartbeats are written to stdout.
func (r *HeadlessRenderer) Run(eng engine.Runner, cfg *config.Config, specs []httpreader.RequestSpec, opts RunOptions) error {
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

	// cmdCh stays nil (never assigned) when the listener is disabled — a
	// nil channel blocks forever in the select below, so the cmdCh arm
	// simply never fires and no separate on/off branch is needed there.
	var cmdCh chan controlCommand
	if r.controlMode() == "stdin" {
		cmdCh = make(chan controlCommand, controlCmdChanCap)
		go controlReader(r.controlInput(), cmdCh)
	}

	if opts.Snapping {
		r.emit(HeartbeatPayload{
			Time:    now(),
			Event:   "snap",
			Message: fmt.Sprintf("📸 Snapping → %s", opts.SnapDir),
		})
	}

	stages := make([]StageInfo, len(cfg.Stages))
	prevRPS := 0
	for i, s := range cfg.Stages {
		stages[i] = StageInfo{
			Name:            s.Label(prevRPS),
			DurationSeconds: s.Duration.Seconds(),
			TargetRPS:       s.TargetRPS,
		}
		prevRPS = s.TargetRPS
	}

	profileName := cfg.ConfigSection.ProfileName
	startedMsg := fmt.Sprintf("Load test started — %d stage(s)", len(cfg.Stages))
	var profileScale float64
	if profileName != "" {
		profileScale = cfg.ConfigSection.ProfileScale
		startedMsg = fmt.Sprintf("Load test started — profile=%s (scale=%.2f) — %d stage(s)",
			profileName, profileScale, len(cfg.Stages))
	}

	r.emit(HeartbeatPayload{
		Time:            now(),
		Event:           "started",
		TotalStages:     len(cfg.Stages),
		Stages:          stages,
		Profile:         profileName,
		ProfileScale:    profileScale,
		Message:         startedMsg,
		ProtocolVersion: protocolVersion,
		Capabilities:    r.capabilities(),
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
	// stopRequested is set by a "stop" control command. Treated like
	// interrupted for the post-loop error branch (log and still finalize,
	// never fail the run) but produces a single "stopped" terminal event
	// instead of "interrupted"+"finished" (§3.3).
	stopRequested := false

loop:
	for {
		select {
		case <-sigCh:
			interrupted = true
			cancel()
			r.emitMessage("interrupted", "Run interrupted by signal")
			break loop

		case cmd := <-cmdCh:
			if cmd.Err != nil {
				r.emit(HeartbeatPayload{
					Time:    now(),
					Event:   "error",
					ID:      cmd.Err.ID,
					Command: cmd.Err.Command,
					Reason:  cmd.Err.Reason,
					Message: cmd.Err.Message,
				})
				continue
			}
			switch cmd.Command {
			case "bias":
				eng.ApplyBias(cmd.Amount)
				cumulative := eng.GetBias()
				r.biasEvents = append(r.biasEvents, snap.BiasEvent{
					Amount:     cmd.Amount,
					Cumulative: cumulative,
					ElapsedS:   elapsedSince(eng.GetStartTime()),
				})
				r.emit(HeartbeatPayload{
					Time:    now(),
					Event:   "ack",
					ID:      cmd.ID,
					Command: cmd.Command,
					Bias:    cumulative,
					Message: fmt.Sprintf("bias %+d applied (cumulative %+d)", cmd.Amount, cumulative),
				})
			case "mark":
				elapsed := elapsedSince(eng.GetStartTime())
				r.marks = append(r.marks, snap.Mark{Label: cmd.Label, ElapsedS: elapsed})
				// The mark event itself is the reply — no separate ack (§2.3).
				r.emit(HeartbeatPayload{
					Time:     now(),
					Event:    "mark",
					ID:       cmd.ID,
					Label:    cmd.Label,
					ElapsedS: elapsed,
				})
			case "stop":
				r.emit(HeartbeatPayload{
					Time:    now(),
					Event:   "ack",
					ID:      cmd.ID,
					Command: cmd.Command,
					Message: "stop received",
				})
				stopRequested = true
				cancel()
				break loop
			}

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
				Bias:         m.Bias,
			})
		}
	}

	// If the loop exited via the signal or ticker path (not engineDone), wait
	// here until RunStages actually returns. This guarantees that no engine
	// worker is still calling recorder.Record() when we call OnRunComplete /
	// finalizeSnapResult below — preventing a recorder data-race on early quit.
	if !engineFinished {
		if err := <-engineDone; err != nil && !errors.Is(err, context.Canceled) {
			if interrupted || stopRequested {
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
	// "stopped" replaces "finished" as the terminal event for a stop-ended
	// run — a single terminal event, not a second one alongside it (§3.3).
	terminalEvent := "finished"
	if stopRequested {
		terminalEvent = "stopped"
	}
	if opts.OnRunComplete != nil {
		status := opts.OnRunComplete()
		if status != "" {
			r.emitMessage(terminalEvent, status)
		}
	} else {
		m := eng.GetMetrics()
		r.emit(HeartbeatPayload{
			Time:         now(),
			Event:        terminalEvent,
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
		case "started", "finished", "stopped", "interrupted", "snap":
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
		case "mark":
			// No Message on this event by design (§2.3) — Label/ElapsedS
			// already say everything, so the text line is built from those.
			_, _ = fmt.Fprintf(os.Stdout, "[%s] mark %q (t+%.1fs)\n", p.Time, p.Label, p.ElapsedS)
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

// elapsedSince returns seconds since start, or 0 if start is the zero time —
// a command can reach here before eng.GetStartTime() is set (engine and
// controlReader both start via unordered `go`), and time.Since would saturate.
func elapsedSince(start time.Time) float64 {
	if start.IsZero() {
		return 0
	}
	return time.Since(start).Seconds()
}
