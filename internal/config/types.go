package config

import "time"

type Config struct {
	ConfigSection Section `yaml:"config"`
	Stages        []Stage `yaml:"stages"`
}

type Section struct {
	HTTPFile       string  `yaml:"httpFile"`
	HTTPFilePath   string  `yaml:"-"` // resolved absolute path, not from yaml
	Prometheus     bool    `yaml:"prometheus"`
	PrometheusPort int     `yaml:"prometheus_port"`
	BreakerPct     float64 `yaml:"breaker_threshold_pct"`
	// Jitter adds organic noise to the RPS ticker.
	// 0.1 means ±10% of the current target RPS. 0 disables jitter (default).
	Jitter float64 `yaml:"jitter"`
	// TimeScale speeds up or slows down the stage clock.
	// 1.0 is real-time (default). 2.0 runs twice as fast. 0.5 runs at half speed.
	TimeScale float64 `yaml:"time_scale"`
}

// Stage represents a single load phase in the test plan.
// The engine interpolates (lerps) from the previous stage's target_rps to
// this stage's target_rps over this stage's duration.
//
// Special cases:
//   - duration: 0s  → instant step (spike / instant ramp-down)
//   - target_rps: 0 → ramp down to zero (cool-down)
type Stage struct {
	// Name is an optional human-readable label shown in the TUI timeline.
	// If omitted, the engine infers a label (Ramp Up, Sustain, Spike, etc.)
	Name      string        `yaml:"name"`
	Duration  time.Duration `yaml:"duration"`
	TargetRPS int           `yaml:"target_rps"`
}

// Label returns the display name for the stage. If Name is set it is used
// directly; otherwise a label is inferred from the stage shape.
func (s Stage) Label(prevTargetRPS int) string {
	if s.Name != "" {
		return s.Name
	}
	switch {
	case s.Duration == 0:
		return "Spike"
	case s.TargetRPS == 0:
		return "Ramp Down"
	case s.TargetRPS > prevTargetRPS:
		return "Ramp Up"
	case s.TargetRPS == prevTargetRPS:
		return "Sustain"
	default:
		return "Ramp Down"
	}
}

func (c *Config) Validate() error {
	if c.ConfigSection.HTTPFile == "" {
		return ErrMissingHttpFile
	}

	if c.ConfigSection.BreakerPct < 0 || c.ConfigSection.BreakerPct > 100 {
		return ErrInvalidBreakerThreshold
	}

	if c.ConfigSection.Jitter < 0 || c.ConfigSection.Jitter > 1 {
		return ErrInvalidJitter
	}

	if c.ConfigSection.TimeScale < 0 {
		return ErrInvalidTimeScale
	}

	if len(c.Stages) == 0 {
		return ErrNoStagesDefined
	}

	for i, stage := range c.Stages {
		// duration < 0 is never valid; duration == 0 is allowed for spike stages
		if stage.Duration < 0 {
			return NewErrInvalidStageDuration(i)
		}

		// target_rps < 0 is never valid; 0 is allowed for cool-down stages
		if stage.TargetRPS < 0 {
			return NewErrInvalidStageTargetRPS(i)
		}
	}

	return nil
}

// TotalDuration returns the sum of all stage durations, scaled by TimeScale.
func (c *Config) TotalDuration() time.Duration {
	var total time.Duration
	for _, s := range c.Stages {
		total += s.Duration
	}
	scale := c.ConfigSection.TimeScale
	if scale <= 0 {
		scale = 1.0
	}
	return time.Duration(float64(total) / scale)
}

// PeakRPS returns the highest target_rps across all stages.
func (c *Config) PeakRPS() int {
	peak := 0
	for _, s := range c.Stages {
		if s.TargetRPS > peak {
			peak = s.TargetRPS
		}
	}
	return peak
}
