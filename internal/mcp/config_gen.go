package mcp

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/profile"
)

// ConfigStage is one load stage of a generated config.
type ConfigStage struct {
	Name      string `json:"name,omitempty" jsonschema:"optional label shown in the timeline"`
	Duration  string `json:"duration" jsonschema:"Go duration such as 30s, 2m or 1h; 0s makes an instant jump"`
	TargetRPS int    `json:"target_rps" jsonschema:"requests per second reached at the end of the stage"`
}

// ConfigSnap tunes snapshot sampling in a generated config.
type ConfigSnap struct {
	SampleRate float64 `json:"sample_rate,omitempty" jsonschema:"fraction of responses sampled for schema inference, 0 to 1"`
	MaxSamples int     `json:"max_samples,omitempty" jsonschema:"per-endpoint sample cap"`
	MaxBodyKB  int     `json:"max_body_kb,omitempty" jsonschema:"per-endpoint stored-body budget in KB"`
}

// ConfigParams are the inputs to generateConfig.
type ConfigParams struct {
	HTTPFile  string        `json:"http_file" jsonschema:"path of the .http file the run will use, relative to where the config is saved"`
	Profile   string        `json:"profile,omitempty" jsonschema:"built-in profile name (see gg://profiles/catalog); expanded into stages. Give either profile or stages"`
	PeakRPS   int           `json:"peak_rps,omitempty" jsonschema:"peak RPS when using a profile; default is the profile's own"`
	Duration  string        `json:"duration,omitempty" jsonschema:"total duration when using a profile, e.g. 5m; default is the profile's own"`
	Stages    []ConfigStage `json:"stages,omitempty" jsonschema:"explicit stages instead of a profile"`
	Jitter    float64       `json:"jitter,omitempty" jsonschema:"0 to 1, random noise on pacing"`
	TimeScale float64       `json:"time_scale,omitempty" jsonschema:"stage clock multiplier, e.g. 10 to run a plan ten times faster"`
	Snap      *ConfigSnap   `json:"snap,omitempty" jsonschema:"snapshot sampling settings"`
}

// ConfigResult is the outcome of generateConfig.
type ConfigResult struct {
	OK       bool     `json:"ok"`
	YAML     string   `json:"yaml,omitempty"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
	Summary  string   `json:"summary,omitempty"`
}

type yamlConfig struct {
	Config yamlSection `yaml:"config"`
	Snap   *yamlSnap   `yaml:"snap,omitempty"`
	Stages []yamlStage `yaml:"stages"`
}

type yamlSection struct {
	HTTPFile  string  `yaml:"httpFile"`
	Jitter    float64 `yaml:"jitter,omitempty"`
	TimeScale float64 `yaml:"time_scale,omitempty"`
}

type yamlSnap struct {
	SampleRate float64 `yaml:"sample_rate,omitempty"`
	MaxSamples int     `yaml:"max_samples,omitempty"`
	MaxBodyKB  int     `yaml:"max_body_kb,omitempty"`
}

type yamlStage struct {
	Name      string `yaml:"name,omitempty"`
	Duration  string `yaml:"duration"`
	TargetRPS int    `yaml:"target_rps"`
}

// generateConfig builds a gg config and validates it with the same rules the
// CLI applies on load, returning errors instead of writing an invalid file.
func generateConfig(p ConfigParams, l Limits) ConfigResult {
	res := ConfigResult{Errors: []string{}, Warnings: []string{}}
	fail := func(format string, a ...any) { res.Errors = append(res.Errors, fmt.Sprintf(format, a...)) }

	if strings.TrimSpace(p.HTTPFile) == "" {
		fail("http_file is required")
	}
	if p.Profile != "" && len(p.Stages) > 0 {
		fail("give either profile or stages, not both")
	}
	if p.Profile == "" && len(p.Stages) == 0 {
		fail("give a profile (see gg://profiles/catalog) or at least one stage")
	}
	if p.Profile == "" && (p.PeakRPS != 0 || p.Duration != "") {
		fail("peak_rps and duration apply to a profile; with explicit stages put them in the stages")
	}

	jitter := p.Jitter
	var stages []config.Stage
	switch {
	case p.Profile != "":
		stages, jitter = profileStages(p, jitter, &res)
	default:
		for i, s := range p.Stages {
			d, err := time.ParseDuration(s.Duration)
			if err != nil {
				fail("stages[%d].duration %q is not a valid duration (use e.g. 30s, 2m)", i, s.Duration)
				continue
			}
			stages = append(stages, config.Stage{Name: s.Name, Duration: d, TargetRPS: s.TargetRPS})
		}
	}
	if len(res.Errors) > 0 {
		return res
	}

	cfg := config.Config{
		ConfigSection: config.Section{HTTPFile: p.HTTPFile, Jitter: jitter, TimeScale: p.TimeScale},
		Stages:        stages,
	}
	if p.Snap != nil {
		cfg.Snap = config.SnapSection{SampleRate: p.Snap.SampleRate, MaxSamples: p.Snap.MaxSamples, MaxBodyKB: p.Snap.MaxBodyKB}
	}
	if err := cfg.Validate(); err != nil {
		fail("%v", err)
		return res
	}

	out := yamlConfig{Config: yamlSection{HTTPFile: p.HTTPFile, Jitter: jitter, TimeScale: p.TimeScale}}
	if p.Snap != nil && cfg.Snap != (config.SnapSection{}) {
		out.Snap = &yamlSnap{SampleRate: p.Snap.SampleRate, MaxSamples: p.Snap.MaxSamples, MaxBodyKB: p.Snap.MaxBodyKB}
	}
	for _, s := range stages {
		out.Stages = append(out.Stages, yamlStage{Name: s.Name, Duration: compactDuration(s.Duration), TargetRPS: s.TargetRPS})
	}
	b, err := yaml.Marshal(out)
	if err != nil {
		fail("render yaml: %v", err)
		return res
	}

	res.OK, res.YAML = true, string(b)
	peak, total := cfg.PeakRPS(), cfg.TotalDuration()
	res.Summary = fmt.Sprintf("%d stages, peak %d RPS, runs for %s", len(stages), peak, compactDuration(total))
	if l.MaxRPS > 0 && peak > l.MaxRPS {
		res.Warnings = append(res.Warnings, fmt.Sprintf("peak %d RPS is above this server's --max-rps %d; start_simulation will refuse it", peak, l.MaxRPS))
	}
	if l.MaxDuration > 0 && total > l.MaxDuration {
		res.Warnings = append(res.Warnings, fmt.Sprintf("run time %s is above this server's --max-duration %s; start_simulation will refuse it", compactDuration(total), l.MaxDuration))
	}
	return res
}

func profileStages(p ConfigParams, jitter float64, res *ConfigResult) ([]config.Stage, float64) {
	prof, err := profile.LoadBuiltIn(p.Profile)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("unknown profile %q; see gg://profiles/catalog", p.Profile))
		return nil, jitter
	}
	peak, dur := prof.DefaultPeakRPS, prof.DefaultDuration
	if p.PeakRPS < 0 {
		res.Errors = append(res.Errors, "peak_rps must be positive")
	} else if p.PeakRPS > 0 {
		peak = p.PeakRPS
	}
	if p.Duration != "" {
		d, err := time.ParseDuration(p.Duration)
		if err != nil || d <= 0 {
			res.Errors = append(res.Errors, fmt.Sprintf("duration %q is not a valid positive duration (use e.g. 5m)", p.Duration))
		} else {
			dur = d
		}
	}
	if len(res.Errors) > 0 {
		return nil, jitter
	}
	if jitter == 0 {
		jitter = prof.ConfigOverride.Jitter
	}
	stages := profile.InflateSegments(prof, peak, dur)
	for i := range stages {
		stages[i].Duration = stages[i].Duration.Round(time.Millisecond)
	}
	return stages, jitter
}

// compactDuration renders d without redundant zero units: 1m0s becomes 1m.
func compactDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}
