package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shyam-s00/gopher-glide/internal/autopilot"
	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
)

const (
	actionAdjust = "adjust_rps"
	actionSet    = "set_rps"
	actionMark   = "mark"
	actionHold   = "hold_rps"
	actionFree   = "release_hold"
	actionAPOn   = "autopilot_start"
	actionAPOff  = "autopilot_stop"

	maxReadingAge = 3 * time.Second
	lagNote       = "applies within about 1 s; the profile may keep ramping, so check get_live_telemetry for the resulting rate"
)

// SteerInput are the arguments of steer_traffic.
type SteerInput struct {
	SessionID  string          `json:"session_id,omitempty" jsonschema:"id returned by start_simulation; may be omitted when only one simulation exists"`
	Action     string          `json:"action" jsonschema:"adjust_rps (change the rate by delta), set_rps (aim for target_rps), hold_rps (pin the rate at target_rps, ignoring the profile's ramp, until release_hold), release_hold (return to the profile), mark (label a moment in the run), autopilot_start (hand the rate to the built-in slo-guard; give autopilot.mode) or autopilot_stop (take the rate back; it stays where the autopilot left it)"`
	Delta      int             `json:"delta,omitempty" jsonschema:"adjust_rps only: signed change in requests per second, e.g. 50 or -20; not zero"`
	TargetRPS  int             `json:"target_rps,omitempty" jsonschema:"set_rps and hold_rps: the rate to aim for or pin, from 1 up to the server's --max-rps"`
	Label      string          `json:"label,omitempty" jsonschema:"mark only: short text saved with the run, e.g. 'p99 spike after cache flush'"`
	AtElapsedS *float64        `json:"at_elapsed_s,omitempty" jsonschema:"mark only: when it happened, in seconds since the run started (use elapsed_s from the telemetry you saw it in); default is now"`
	Autopilot  *AutopilotInput `json:"autopilot,omitempty" jsonschema:"autopilot_start only: mode must be slo-guard (find-ceiling owns the run's schedule, so it can only be given to start_simulation), with optional max_p99_ms and max_error_rate"`
}

// SteerOutput is the result of steer_traffic.
type SteerOutput struct {
	SessionID string         `json:"session_id"`
	Origin    session.Origin `json:"origin"`
	Action    string         `json:"action"`
	// Applied is false only when set_rps found the rate already at the target.
	Applied bool `json:"applied"`

	Delta           *int `json:"delta,omitempty"`
	BiasTotal       *int `json:"bias_total,omitempty"`
	TargetRPSBefore int  `json:"target_rps_before,omitempty"`
	// HoldRPS is the pinned rate after hold_rps; absent after release_hold.
	HoldRPS *int `json:"hold_rps,omitempty"`

	Label    string   `json:"label,omitempty"`
	ElapsedS *float64 `json:"elapsed_s,omitempty"`
	Clamped  bool     `json:"clamped,omitempty"`
	// Autopilot is the controller handed the rate (autopilot_start) or "stopped".
	Autopilot string `json:"autopilot,omitempty"`

	Note    string `json:"note,omitempty"`
	Warning string `json:"warning,omitempty"`
}

func registerSteerTool(s *sdk.Server, mgr *session.Manager, l Limits) {
	sdk.AddTool(s, &sdk.Tool{
		Name: "steer_traffic",
		Description: "Change a running simulation or annotate it. action adjust_rps changes the rate by a signed delta; set_rps aims for an absolute target_rps; " +
			"hold_rps pins the rate flat at target_rps regardless of the profile's ramp (the run still ends on schedule) and release_hold returns to the profile; " +
			"mark labels the current moment (or at_elapsed_s, a time you saw in telemetry) so the snapshot timeline explains what you did and why. " +
			"autopilot_start hands the rate to gg's built-in slo-guard, which clamps the rate within a second of a p99 or error-rate breach and releases it in steps once health recovers (no LLM cost); " +
			"while it runs, adjust_rps, set_rps, hold_rps and release_hold are refused, and autopilot_stop takes the rate back (it stays where the autopilot left it, so release_hold or hold_rps next). " +
			"adjust_rps and set_rps are offsets on top of the profile, take effect within about 1 s and are limited by the server's --max-rps; " +
			"the result gives target_rps_before and the new bias_total, not a promised rate. Only works while the run is running; recent_steers in get_live_telemetry lists what you already did. " +
			"A run the user started from the IDE (origin user) refuses steering with user_session_locked unless the user allowed it; telemetry's agent_steer_allowed says which. " +
			"This changes the load on the target.",
		Annotations: &sdk.ToolAnnotations{DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(true), Title: "Steer traffic"},
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in SteerInput) (*sdk.CallToolResult, SteerOutput, error) {
		out, err := steer(ctx, mgr, l, in)
		return nil, out, err
	})
}

func boolPtr(b bool) *bool { return &b }

func steer(ctx context.Context, mgr *session.Manager, l Limits, in SteerInput) (SteerOutput, error) {
	if err := checkSteerInput(in); err != nil {
		return SteerOutput{}, err
	}
	if in.Action == actionHold && (in.TargetRPS < 1 || in.TargetRPS > l.MaxRPS) {
		return SteerOutput{}, fmt.Errorf("target_rps must be between 1 and %d (the server's limit, raise it with --max-rps)", l.MaxRPS)
	}
	sess, err := mgr.Resolve(in.SessionID)
	if err != nil {
		return SteerOutput{}, err
	}
	out := SteerOutput{SessionID: sess.ID, Origin: sess.Origin(), Action: in.Action}
	if err := sess.AgentAccess(); err != nil {
		return SteerOutput{}, err
	}
	if (in.Action == actionHold || in.Action == actionFree) && sess.AutopilotActive() {
		return SteerOutput{}, errors.New("the autopilot owns the rate right now; call steer_traffic with autopilot_stop first to take over")
	}

	if in.Action == actionMark {
		res, err := sess.Mark(ctx, in.Label, in.AtElapsedS)
		if err != nil {
			return SteerOutput{}, err
		}
		out.Applied, out.Label, out.ElapsedS, out.Clamped, out.Warning = true, res.Label, &res.ElapsedS, res.Clamped, res.Warning
		return out, nil
	}

	switch in.Action {
	case actionAPOn:
		a := in.Autopilot
		if err := sess.StartAutopilot(ctx, session.AutopilotArgs{Mode: a.Mode, MaxP99Ms: a.MaxP99Ms, MaxErrorRate: a.MaxErrorRate}); err != nil {
			return SteerOutput{}, err
		}
		out.Applied, out.Autopilot = true, a.Mode
		out.Note = "slo-guard is watching; its state and any clamp show in get_live_telemetry (autopilot, hold_rps) and as marks on the run"
		return out, nil
	case actionAPOff:
		if err := sess.StopAutopilot(ctx); err != nil {
			return SteerOutput{}, err
		}
		out.Applied, out.Autopilot = true, "stopped"
		out.Note = "the rate stays where the autopilot left it (hold_rps in get_live_telemetry); call release_hold to rejoin the profile or hold_rps to pin a new rate"
		return out, nil
	case actionHold:
		res, err := sess.Hold(ctx, in.TargetRPS)
		if err != nil {
			return SteerOutput{}, err
		}
		out.Applied, out.HoldRPS, out.Note = true, &res.HoldRPS, "takes effect within about 1 s; the profile's stage clock keeps running, so the run still ends on schedule. Call release_hold to rejoin the profile"
		return out, nil
	case actionFree:
		if err := sess.Release(ctx); err != nil {
			return SteerOutput{}, err
		}
		out.Applied, out.Note = true, "the rate rejoins the profile at its current point within about 1 s"
		return out, nil
	}

	plan := planAdjust(l, in.Delta)
	if in.Action == actionSet {
		plan = planSet(l, in.TargetRPS)
	}
	res, err := sess.SteerBias(ctx, in.Action, plan)
	if err != nil {
		return SteerOutput{}, err
	}
	out.Applied, out.Delta, out.BiasTotal = res.Delta != 0, &res.Delta, &res.BiasTotal
	out.TargetRPSBefore, out.Note = res.TargetBefore, res.Note
	return out, nil
}

func checkSteerInput(in SteerInput) error {
	if in.Autopilot != nil && in.Action != actionAPOn {
		return errors.New("autopilot is an argument of autopilot_start only")
	}
	switch in.Action {
	case actionAPOn:
		a := in.Autopilot
		switch {
		case a == nil:
			return errors.New("autopilot_start needs autopilot.mode (slo-guard)")
		case a.Mode == string(autopilot.ModeFindCeiling):
			return errors.New("find-ceiling owns the run's schedule, so it can only be started with start_simulation; here use mode slo-guard")
		case a.Mode != string(autopilot.ModeSLOGuard):
			return fmt.Errorf("autopilot.mode must be %q, got %q", autopilot.ModeSLOGuard, a.Mode)
		case a.StartRPS != 0 || a.MaxRPS != 0 || a.MaxDuration != "":
			return errors.New("start_rps, max_rps and max_duration belong to find-ceiling; slo-guard takes only max_p99_ms and max_error_rate")
		}
		if err := checkAutopilotThresholds(a); err != nil {
			return err
		}
		if in.Delta != 0 || in.TargetRPS != 0 || in.Label != "" || in.AtElapsedS != nil {
			return errors.New("autopilot_start takes only autopilot")
		}
	case actionAPOff:
		if in.Delta != 0 || in.TargetRPS != 0 || in.Label != "" || in.AtElapsedS != nil {
			return errors.New("autopilot_stop takes no other arguments")
		}
	case actionAdjust:
		if in.Delta == 0 {
			return errors.New("adjust_rps needs a non-zero delta, e.g. 50 or -20")
		}
		if in.TargetRPS != 0 || in.Label != "" || in.AtElapsedS != nil {
			return errors.New("adjust_rps takes only delta")
		}
	case actionSet:
		if in.Delta != 0 || in.Label != "" || in.AtElapsedS != nil {
			return errors.New("set_rps takes only target_rps")
		}
	case actionHold:
		if in.TargetRPS == 0 {
			return errors.New("hold_rps needs target_rps, the rate to pin")
		}
		if in.Delta != 0 || in.Label != "" || in.AtElapsedS != nil {
			return errors.New("hold_rps takes only target_rps")
		}
	case actionFree:
		if in.Delta != 0 || in.TargetRPS != 0 || in.Label != "" || in.AtElapsedS != nil {
			return errors.New("release_hold takes no other arguments")
		}
	case actionMark:
		if in.Label == "" {
			return errors.New("mark needs a label")
		}
		if in.Delta != 0 || in.TargetRPS != 0 {
			return errors.New("mark takes only label and at_elapsed_s")
		}
	default:
		return fmt.Errorf("unknown action %q; use adjust_rps, set_rps, hold_rps, release_hold, mark, autopilot_start or autopilot_stop", in.Action)
	}
	return nil
}

// planAdjust changes the rate by delta, keeping the bias within the server cap.
func planAdjust(l Limits, delta int) session.Planner {
	return func(r session.Reading) (int, string, error) {
		if !r.Available {
			return 0, "", errors.New("no telemetry reading yet (state is starting); retry in a second")
		}
		if err := heldErr(r); err != nil {
			return 0, "", err
		}
		if err := checkBiasCap(l, r, r.Bias+delta); err != nil {
			return 0, "", err
		}
		note := lagNote
		if r.TargetRPS+(r.Bias-r.BiasAtReading)+delta < 1 {
			note += "; the rate cannot go below 1 RPS"
		}
		return delta, note, nil
	}
}

// planSet aims for target by turning the gap to the latest reading into a delta.
func planSet(l Limits, target int) session.Planner {
	return func(r session.Reading) (int, string, error) {
		if target < 1 || target > l.MaxRPS {
			return 0, "", fmt.Errorf("target_rps must be between 1 and %d (the server's limit, raise it with --max-rps); gg never runs below 1 RPS", l.MaxRPS)
		}
		switch {
		case !r.Available:
			return 0, "", errors.New("no telemetry reading yet (state is starting); retry in a second")
		case r.Autopilot:
			return 0, "", heldErr(r)
		case r.HoldRPS > 0:
			return 0, "", heldErr(r)
		case r.Age > maxReadingAge:
			return 0, "", fmt.Errorf("the latest reading is %.0f s old; check get_live_telemetry and retry", r.Age.Seconds())
		case !r.Settled:
			return 0, "", errors.New("an earlier rate change is still taking effect; retry after the next reading (about 1-2 s)")
		case r.TargetRPS <= 1 && r.Bias < 0:
			return 0, "", fmt.Errorf("the rate is at the 1 RPS floor with a bias of %d, so the profile's own rate is hidden; use adjust_rps with a positive delta (+%d undoes the bias)", r.Bias, -r.Bias)
		}
		delta := target - r.TargetRPS
		if delta == 0 {
			return 0, "already at the target rate", nil
		}
		if err := checkBiasCap(l, r, r.Bias+delta); err != nil {
			return 0, "", err
		}
		return delta, lagNote, nil
	}
}

// heldErr refuses a bias-based change while a hold pins the rate, since it would have no effect.
func heldErr(r session.Reading) error {
	if r.Autopilot {
		return errors.New("the autopilot owns the rate right now; call steer_traffic with autopilot_stop first to take over")
	}
	if r.HoldRPS == 0 {
		return nil
	}
	return fmt.Errorf("the rate is held at %d RPS, so a bias change would not move it; call hold_rps with a new target_rps, or release_hold first", r.HoldRPS)
}

// checkBiasCap keeps the plan's peak plus the bias at or under the server's RPS cap.
func checkBiasCap(l Limits, r session.Reading, newBias int) error {
	peak := r.PeakRPS
	if peak+newBias > l.MaxRPS {
		return fmt.Errorf("a bias of %+d would put the run's peak (%d RPS) at %d RPS, above the server limit of %d RPS (raise it with --max-rps)",
			newBias, peak, peak+newBias, l.MaxRPS)
	}
	return nil
}
