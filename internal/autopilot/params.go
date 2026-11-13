// Package autopilot is a deterministic closed-loop traffic controller. It runs
// inside the process that owns the engine and steers the request rate with the
// engine's hold override, judging each decision on windowed health only.
package autopilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Mode selects what the controller does.
type Mode string

const (
	// ModeFindCeiling steps the rate up until health breaches, then confirms the
	// highest sustainable rate. It owns the run schedule.
	ModeFindCeiling Mode = "find-ceiling"
	// ModeSLOGuard runs the caller's schedule and clamps the rate only on breach.
	ModeSLOGuard Mode = "slo-guard"
)

// State is where the controller is in its loop.
type State string

const (
	StateStepping   State = "stepping"
	StateBreached   State = "breached"
	StateConfirming State = "confirming"
	StateClamped    State = "clamped"
	StateReleasing  State = "releasing"
	StateWatching   State = "watching"
	StateDone       State = "done"
)

// Outcome is the final result of a controller run.
type Outcome string

const (
	OutcomeCeilingFound      Outcome = "ceiling_found"
	OutcomeCeilingNotReached Outcome = "ceiling_not_reached"
	OutcomeUnstable          Outcome = "unstable"
	OutcomeStopped           Outcome = "stopped"
	OutcomeCompleted         Outcome = "completed"
)

// FindCeilingMargin is how much longer than MaxDuration a find-ceiling run's
// stage lasts, so the controller ends the run itself and can say why.
const FindCeilingMargin = 15 * time.Second

// Defaults applied by WithDefaults to zero-valued fields.
const (
	DefaultMaxP99Ms     = 250.0
	DefaultMaxErrorRate = 0.01
	DefaultMinSamples   = 50
	DefaultSettle       = 2 * time.Second
	DefaultStepInterval = 5 * time.Second
	DefaultStartRPS     = 20
	DefaultStepPct      = 25
	DefaultConfirmHold  = 15 * time.Second
	DefaultMaxDuration  = 5 * time.Minute
	DefaultMaxRPS       = 1000
	DefaultRecoverHold  = 10 * time.Second
)

// Params configures a controller. Zero values mean "use the default".
type Params struct {
	Mode         Mode
	MaxP99Ms     float64
	MaxErrorRate float64
	// MinSamples is how many latency samples a window needs before it is judged.
	MinSamples int
	// Settle is skipped after each rate change before measuring (a hold lands
	// on the engine's next one-second window).
	Settle       time.Duration
	StepInterval time.Duration

	// find-ceiling
	StartRPS    int
	StepPct     int // percent step; ignored when StepRPS is set
	StepRPS     int
	MaxRPS      int
	ConfirmHold time.Duration
	MaxDuration time.Duration

	// slo-guard
	RecoverHold time.Duration
}

// WithDefaults returns p with every zero field replaced by its default.
func (p Params) WithDefaults() Params {
	if p.MaxP99Ms == 0 {
		p.MaxP99Ms = DefaultMaxP99Ms
	}
	if p.MaxErrorRate == 0 {
		p.MaxErrorRate = DefaultMaxErrorRate
	}
	if p.MinSamples == 0 {
		p.MinSamples = DefaultMinSamples
	}
	if p.Settle == 0 {
		p.Settle = DefaultSettle
	}
	if p.StepInterval == 0 {
		p.StepInterval = DefaultStepInterval
	}
	switch p.Mode {
	case ModeFindCeiling:
		if p.StartRPS == 0 {
			p.StartRPS = DefaultStartRPS
		}
		if p.StepPct == 0 && p.StepRPS == 0 {
			p.StepPct = DefaultStepPct
		}
		if p.MaxRPS == 0 {
			p.MaxRPS = DefaultMaxRPS
		}
		if p.ConfirmHold == 0 {
			p.ConfirmHold = DefaultConfirmHold
		}
		if p.MaxDuration == 0 {
			p.MaxDuration = DefaultMaxDuration
		}
	case ModeSLOGuard:
		if p.RecoverHold == 0 {
			p.RecoverHold = DefaultRecoverHold
		}
	}
	return p
}

// Validate checks a defaulted Params.
func (p Params) Validate() error {
	switch p.Mode {
	case ModeFindCeiling, ModeSLOGuard:
	default:
		return fmt.Errorf("autopilot mode must be %q or %q, got %q", ModeFindCeiling, ModeSLOGuard, p.Mode)
	}
	var errs []error
	if p.MaxP99Ms <= 0 {
		errs = append(errs, errors.New("max p99 must be greater than 0"))
	}
	if p.MaxErrorRate <= 0 || p.MaxErrorRate > 1 {
		errs = append(errs, errors.New("max error rate must be in (0, 1]"))
	}
	if p.MinSamples < 1 {
		errs = append(errs, errors.New("min samples must be at least 1"))
	}
	if p.Settle < 0 || p.StepInterval < time.Second {
		errs = append(errs, errors.New("step interval must be at least 1s and settle must not be negative"))
	}
	if p.Mode == ModeFindCeiling {
		if p.StartRPS < 1 {
			errs = append(errs, errors.New("start rps must be at least 1"))
		}
		if p.MaxRPS < p.StartRPS {
			errs = append(errs, errors.New("max rps must not be below start rps"))
		}
		if p.StepRPS < 0 || p.StepPct < 0 || (p.StepRPS == 0 && p.StepPct == 0) {
			errs = append(errs, errors.New("step must be a positive percentage or rps amount"))
		}
		if p.ConfirmHold < time.Second || p.MaxDuration < time.Second {
			errs = append(errs, errors.New("confirm hold and max duration must be at least 1s"))
		}
	} else if p.RecoverHold < time.Second {
		errs = append(errs, errors.New("recover hold must be at least 1s"))
	}
	return errors.Join(errs...)
}

// MarshalJSON writes durations as seconds, matching the control protocol.
func (p Params) MarshalJSON() ([]byte, error) {
	type wire struct {
		Mode          Mode    `json:"mode"`
		MaxP99Ms      float64 `json:"max_p99_ms"`
		MaxErrorRate  float64 `json:"max_error_rate"`
		MinSamples    int     `json:"min_samples"`
		SettleS       float64 `json:"settle_s"`
		StepIntervalS float64 `json:"step_interval_s"`
		StartRPS      int     `json:"start_rps,omitempty"`
		StepPct       int     `json:"step_pct,omitempty"`
		StepRPS       int     `json:"step_rps,omitempty"`
		MaxRPS        int     `json:"max_rps,omitempty"`
		ConfirmHoldS  float64 `json:"confirm_hold_s,omitempty"`
		MaxDurationS  float64 `json:"max_duration_s,omitempty"`
		RecoverHoldS  float64 `json:"recover_hold_s,omitempty"`
	}
	return json.Marshal(wire{
		Mode: p.Mode, MaxP99Ms: p.MaxP99Ms, MaxErrorRate: p.MaxErrorRate, MinSamples: p.MinSamples,
		SettleS: p.Settle.Seconds(), StepIntervalS: p.StepInterval.Seconds(),
		StartRPS: p.StartRPS, StepPct: p.StepPct, StepRPS: p.StepRPS, MaxRPS: p.MaxRPS,
		ConfirmHoldS: p.ConfirmHold.Seconds(), MaxDurationS: p.MaxDuration.Seconds(),
		RecoverHoldS: p.RecoverHold.Seconds(),
	})
}
