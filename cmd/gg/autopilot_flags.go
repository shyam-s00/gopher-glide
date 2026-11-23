package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/autopilot"
	"github.com/shyam-s00/gopher-glide/internal/config"
)

// autopilotFlags are the --autopilot options for the built-in controller.
type autopilotFlags struct {
	mode         string
	maxP99       time.Duration
	maxErrorRate float64
	maxRPS       int
	startRPS     int
	stepPct      int
	minSamples   int
	stepInterval time.Duration
	confirmHold  time.Duration
	recoverHold  time.Duration
}

func (f *autopilotFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.mode, "autopilot", "", "built-in autopilot: find-ceiling (discover the highest sustainable RPS) | slo-guard (clamp the rate on an SLO breach)")
	fs.DurationVar(&f.maxP99, "max-p99", 0, "autopilot: p99 latency limit, e.g. 200ms (default 250ms)")
	fs.Float64Var(&f.maxErrorRate, "max-error-rate", 0, "autopilot: error-rate limit as a fraction (default 0.01)")
	fs.IntVar(&f.maxRPS, "max-rps", 0, "autopilot find-ceiling: never exceed this rate (default 1000)")
	fs.IntVar(&f.startRPS, "start-rps", 0, "autopilot find-ceiling: first rate to hold (default 20)")
	fs.IntVar(&f.stepPct, "step-pct", 0, "autopilot find-ceiling: percent to raise the rate each step (default 25)")
	fs.IntVar(&f.minSamples, "min-samples", 0, "autopilot: latency samples a window needs before it is judged (default 50)")
	fs.DurationVar(&f.stepInterval, "step-interval", 0, "autopilot: time between rate steps (default 5s)")
	fs.DurationVar(&f.confirmHold, "confirm-hold", 0, "autopilot find-ceiling: how long to confirm the backed-off rate (default 15s)")
	fs.DurationVar(&f.recoverHold, "recover-hold", 0, "autopilot slo-guard: how long health must stay in margin before releasing (default 10s)")
}

// build returns the controller's parameters, or nil when --autopilot is unset.
// find-ceiling owns the schedule, so it replaces cfg.Stages with one flat stage
// sized for the ceiling search; --profile cannot be combined with it.
func (f *autopilotFlags) build(cfg *config.Config, profileSet bool, duration time.Duration) (*autopilot.Params, error) {
	if f.mode == "" {
		return nil, nil
	}
	p := autopilot.Params{
		Mode:         autopilot.Mode(f.mode),
		MaxP99Ms:     float64(f.maxP99) / float64(time.Millisecond),
		MaxErrorRate: f.maxErrorRate,
		MaxRPS:       f.maxRPS,
		StartRPS:     f.startRPS,
		StepPct:      f.stepPct,
		MinSamples:   f.minSamples,
		StepInterval: f.stepInterval,
		ConfirmHold:  f.confirmHold,
		RecoverHold:  f.recoverHold,
		MaxDuration:  duration,
	}.WithDefaults()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Mode != autopilot.ModeFindCeiling {
		return &p, nil
	}
	if profileSet {
		return nil, fmt.Errorf("--autopilot find-ceiling sets its own schedule and cannot be combined with --profile")
	}
	cfg.Stages = []config.Stage{{Name: "Find ceiling", Duration: p.MaxDuration + autopilot.FindCeilingMargin, TargetRPS: p.MaxRPS}}
	return &p, nil
}
