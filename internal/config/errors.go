package config

import (
	"fmt"
)

var (
	ErrMissingHttpFile         = fmt.Errorf("httpFile is required")
	ErrInvalidBreakerThreshold = fmt.Errorf("breaker_threshold_pct must be between 0 and 100")
	ErrNoStagesDefined         = fmt.Errorf("at least one stage must be defined")
)

func NewErrInvalidStageDuration(index int) error {
	return fmt.Errorf("stage %d: duration must be greater than 0", index)
}

func NewErrInvalidStageTargetVPU(index int) error {
	return fmt.Errorf("stage %d: target_vpu must be greater than 0", index)
}
