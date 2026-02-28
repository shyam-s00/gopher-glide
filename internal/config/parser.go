package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	DefaultPrometheusPort = 9090
)

func Load(path string) (*Config, error) {
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
