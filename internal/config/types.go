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
}

type Stage struct {
	Duration  time.Duration `yaml:"duration"`
	TargetRPS int           `yaml:"target_rps"`
}

func (c *Config) Validate() error {
	if c.ConfigSection.HTTPFile == "" {
		return ErrMissingHttpFile
	}

	if c.ConfigSection.BreakerPct < 0 || c.ConfigSection.BreakerPct > 100 {
		return ErrInvalidBreakerThreshold
	}

	if len(c.Stages) == 0 {
		return ErrNoStagesDefined
	}

	for i, stage := range c.Stages {
		if stage.Duration <= 0 {
			return NewErrInvalidStageDuration(i)
		}

		if stage.TargetRPS <= 0 {
			return NewErrInvalidStageTargetRPS(i)
		}
	}

	return nil
}
