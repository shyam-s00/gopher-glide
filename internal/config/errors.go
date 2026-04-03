package config

import (
	"fmt"
)

var (
	ErrMissingHttpFile         = fmt.Errorf("httpFile is required")
	ErrInvalidBreakerThreshold = fmt.Errorf("breaker_threshold_pct must be between 0 and 100")
	ErrNoStagesDefined         = fmt.Errorf("at least one stage must be defined")
	ErrInvalidJitter           = fmt.Errorf("jitter must be between 0 and 1 (e.g. 0.1 for ±10%%)")
	ErrInvalidTimeScale        = fmt.Errorf("time_scale must be greater than or equal to 0")

	// Snap section validation errors.
	ErrInvalidSnapSampleRate = fmt.Errorf("snap.sample_rate must be between 0.0 and 1.0")
	ErrInvalidSnapMaxSamples = fmt.Errorf("snap.max_samples must be >= 0 (0 = use application default of 200)")
	ErrInvalidSnapMaxBodyKB  = fmt.Errorf("snap.max_body_kb must be >= 0 (0 = no byte-based limit)")
)

func NewErrInvalidStageDuration(index int) error {
	return fmt.Errorf("stage %d: duration must be >= 0 (use 0 for an instant spike step)", index)
}

func NewErrInvalidStageTargetRPS(index int) error {
	return fmt.Errorf("stage %d: target_rps must be >= 0 (use 0 for a cool-down to zero)", index)
}
