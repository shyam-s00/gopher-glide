package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultPrometheusPort = 9090
)

// DefaultConfig returns a minimal in-memory Config used when no config file is
// provided. It defines a simple smoke-test shape (gentle ramp → hold → cool-down).
// HTTPFile is intentionally left empty; callers must populate it (e.g. via
// --http-file) before the config is considered fully valid.
func DefaultConfig() *Config {
	return &Config{
		ConfigSection: Section{
			PrometheusPort: DefaultPrometheusPort,
			ProfileScale:   1.0,
		},
		Stages: []Stage{
			{Name: "Ramp Up", Duration: 30 * time.Second, TargetRPS: 10},
			{Name: "Sustain", Duration: 60 * time.Second, TargetRPS: 10},
			{Name: "Ramp Down", Duration: 30 * time.Second, TargetRPS: 0},
		},
	}
}

// Load reads and parses a YAML config file at path.
// If path is empty, Load returns DefaultConfig() without validation so that
// callers can apply CLI overrides (e.g. --http-file) before validating.
func Load(path string) (*Config, error) {
	if path == "" {
		return DefaultConfig(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if c.ConfigSection.PrometheusPort == 0 {
		c.ConfigSection.PrometheusPort = DefaultPrometheusPort
	}

	if c.ConfigSection.ProfileScale == 0 {
		c.ConfigSection.ProfileScale = 1.0
	}

	// Resolve httpFile relative to the config file's directory.
	// No filesystem path traversal — only the filename is used.
	configDir := filepath.Dir(filepath.Clean(path))
	httpFileName := filepath.Base(c.ConfigSection.HTTPFile)
	c.ConfigSection.HTTPFilePath = filepath.Join(configDir, httpFileName)

	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &c, nil
}
