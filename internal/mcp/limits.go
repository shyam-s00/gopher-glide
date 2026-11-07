package mcp

import (
	"errors"
	"fmt"
	"net/url"
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

// CheckURL rejects a target whose host is not on the allowlist. An empty
// allowlist permits every host.
func (l Limits) CheckURL(u *url.URL) error {
	if len(l.AllowHosts) == 0 {
		return nil
	}
	hostPort, host := strings.ToLower(u.Host), strings.ToLower(u.Hostname())
	for _, a := range l.AllowHosts {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == hostPort || (a == host && !strings.Contains(a, ":")) {
			return nil
		}
	}
	return fmt.Errorf("host %q is not allowed; the server was started with --allow-host %s",
		u.Host, strings.Join(l.AllowHosts, ","))
}

// CheckRun rejects a simulation whose peak rate or length exceeds the caps,
// naming the flag that raises each one.
func (l Limits) CheckRun(peakRPS int, duration time.Duration) error {
	var errs []error
	if peakRPS > l.MaxRPS {
		errs = append(errs, fmt.Errorf("peak %d RPS is above the server limit of %d RPS (raise it with --max-rps)", peakRPS, l.MaxRPS))
	}
	if duration > l.MaxDuration {
		errs = append(errs, fmt.Errorf("run time %s is above the server limit of %s (raise it with --max-duration)", duration.Round(time.Second), l.MaxDuration))
	}
	return errors.Join(errs...)
}
