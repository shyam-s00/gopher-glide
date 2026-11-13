package mcp

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/autopilot"
)

// defaultFindCeilingDuration is how long find-ceiling may search unless told otherwise.
const defaultFindCeilingDuration = 5 * time.Minute

// autopilotPlan is a validated autopilot request, as flags for the child.
type autopilotPlan struct {
	mode        autopilot.Mode
	args        []string
	maxRPS      int
	maxDuration time.Duration
}

func (p autopilotPlan) findCeiling() bool { return p.mode == autopilot.ModeFindCeiling }

// planAutopilot checks the request against the server's caps. find-ceiling's
// rate and search time default to those caps; asking for more is refused.
func planAutopilot(l Limits, in StartInput) (autopilotPlan, error) {
	a := in.Autopilot
	if a == nil {
		return autopilotPlan{}, nil
	}
	p := autopilotPlan{mode: autopilot.Mode(a.Mode)}
	if p.mode != autopilot.ModeFindCeiling && p.mode != autopilot.ModeSLOGuard {
		return p, fmt.Errorf("autopilot.mode must be %q or %q, got %q", autopilot.ModeFindCeiling, autopilot.ModeSLOGuard, a.Mode)
	}
	if err := checkAutopilotThresholds(a); err != nil {
		return p, err
	}
	p.args = []string{"--autopilot", a.Mode}
	if a.MaxP99Ms > 0 {
		p.args = append(p.args, "--max-p99", (time.Duration(a.MaxP99Ms * float64(time.Millisecond))).String())
	}
	if a.MaxErrorRate > 0 {
		p.args = append(p.args, "--max-error-rate", strconv.FormatFloat(a.MaxErrorRate, 'f', -1, 64))
	}
	if !p.findCeiling() {
		if a.StartRPS != 0 || a.MaxRPS != 0 || a.MaxDuration != "" {
			return p, errors.New("start_rps, max_rps and max_duration belong to find-ceiling; slo-guard follows the profile or config you gave")
		}
		return p, nil
	}

	p.maxRPS = a.MaxRPS
	switch {
	case a.MaxRPS < 0:
		return p, errors.New("autopilot.max_rps must be positive")
	case a.MaxRPS == 0:
		p.maxRPS = l.MaxRPS
	}
	if a.StartRPS < 0 || a.StartRPS > p.maxRPS {
		return p, fmt.Errorf("autopilot.start_rps must be between 1 and max_rps (%d)", p.maxRPS)
	}
	p.maxDuration = min(defaultFindCeilingDuration, l.MaxDuration-autopilot.FindCeilingMargin)
	if a.MaxDuration != "" {
		d, err := time.ParseDuration(a.MaxDuration)
		if err != nil || d <= 0 {
			return p, fmt.Errorf("autopilot.max_duration %q is not a valid positive duration (use e.g. 90s, 5m)", a.MaxDuration)
		}
		p.maxDuration = d
	}
	if p.maxDuration < 30*time.Second {
		return p, fmt.Errorf("find-ceiling needs at least 30s to search; the server's --max-duration is %s (it adds %s of margin)", l.MaxDuration, autopilot.FindCeilingMargin)
	}
	p.args = append(p.args, "--max-rps", strconv.Itoa(p.maxRPS), "--duration", p.maxDuration.String())
	if a.StartRPS > 0 {
		p.args = append(p.args, "--start-rps", strconv.Itoa(a.StartRPS))
	}
	return p, nil
}

func checkAutopilotThresholds(a *AutopilotInput) error {
	if a.MaxP99Ms < 0 || math.IsNaN(a.MaxP99Ms) || math.IsInf(a.MaxP99Ms, 0) {
		return errors.New("autopilot.max_p99_ms must be positive")
	}
	if a.MaxErrorRate < 0 || a.MaxErrorRate > 1 || math.IsNaN(a.MaxErrorRate) {
		return errors.New("autopilot.max_error_rate must be between 0 and 1")
	}
	return nil
}
