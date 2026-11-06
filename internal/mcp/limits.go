package mcp

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Limits are the safety caps a server enforces on every simulation it starts.
type Limits struct {
	MaxRPS      int
	MaxDuration time.Duration
	MaxSessions int
	// AllowHosts restricts targets to these hosts; empty means any host.
	AllowHosts []string
}

// DefaultLimits returns the caps used when no flag overrides them.
func DefaultLimits() Limits {
	return Limits{
		MaxRPS:      1000,
		MaxDuration: 10 * time.Minute,
		MaxSessions: 2,
	}
}

// Validate rejects caps that would make the server unusable or unbounded.
func (l Limits) Validate() error {
	var errs []error
	if l.MaxRPS < 1 {
		errs = append(errs, fmt.Errorf("max-rps must be at least 1, got %d", l.MaxRPS))
	}
	if l.MaxDuration <= 0 {
		errs = append(errs, fmt.Errorf("max-duration must be positive, got %s", l.MaxDuration))
	}
	if l.MaxSessions < 1 {
		errs = append(errs, fmt.Errorf("max-sessions must be at least 1, got %d", l.MaxSessions))
	}
	for _, h := range l.AllowHosts {
		if strings.TrimSpace(h) == "" || strings.ContainsAny(h, "/ \t") {
			errs = append(errs, fmt.Errorf("allow-host %q must be a bare host or host:port", h))
		}
	}
	return errors.Join(errs...)
}
