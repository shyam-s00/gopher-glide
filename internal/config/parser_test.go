package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTemp writes content to a temp file with the given name and returns the path.
func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp file %s: %v", path, err)
	}
	return path
}

func TestLoad_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	yaml := `
config:
  httpFile: "request.http"
  prometheus: true
  prometheus_port: 8080
  breaker_threshold_pct: 10.0

stages:
  - duration: 30s
    target_rps: 100
  - duration: 1m
    target_rps: 200
`
	cfgPath := writeTemp(t, dir, "config.yaml", yaml)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ConfigSection.HTTPFile != "request.http" {
		t.Errorf("expected httpFile=request.http, got %s", cfg.ConfigSection.HTTPFile)
	}
	if !cfg.ConfigSection.Prometheus {
		t.Error("expected prometheus=true")
	}
	if cfg.ConfigSection.PrometheusPort != 8080 {
		t.Errorf("expected prometheus_port=8080, got %d", cfg.ConfigSection.PrometheusPort)
	}
	if cfg.ConfigSection.BreakerPct != 10.0 {
		t.Errorf("expected breaker_threshold_pct=10.0, got %f", cfg.ConfigSection.BreakerPct)
	}
	if len(cfg.Stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(cfg.Stages))
	}
	if cfg.Stages[0].Duration != 30*time.Second {
		t.Errorf("expected stage[0].duration=30s, got %v", cfg.Stages[0].Duration)
	}
	if cfg.Stages[0].TargetRPS != 100 {
		t.Errorf("expected stage[0].target_rps=100, got %d", cfg.Stages[0].TargetRPS)
	}
	if cfg.Stages[1].Duration != 60*time.Second {
		t.Errorf("expected stage[1].duration=1m, got %v", cfg.Stages[1].Duration)
	}
	if cfg.Stages[1].TargetRPS != 200 {
		t.Errorf("expected stage[1].target_rps=200, got %d", cfg.Stages[1].TargetRPS)
	}
}

func TestLoad_DefaultPrometheusPort(t *testing.T) {
	dir := t.TempDir()
	yaml := `
config:
  httpFile: "request.http"
  breaker_threshold_pct: 5.0

stages:
  - duration: 10s
    target_rps: 50
`
	cfgPath := writeTemp(t, dir, "config.yaml", yaml)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ConfigSection.PrometheusPort != DefaultPrometheusPort {
		t.Errorf("expected default prometheus_port=%d, got %d", DefaultPrometheusPort, cfg.ConfigSection.PrometheusPort)
	}
}

func TestLoad_HTTPFilePathResolved(t *testing.T) {
	dir := t.TempDir()
	yaml := `
config:
  httpFile: "request.http"
  breaker_threshold_pct: 5.0

stages:
  - duration: 10s
    target_rps: 50
`
	cfgPath := writeTemp(t, dir, "config.yaml", yaml)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join(dir, "request.http")
	if cfg.ConfigSection.HTTPFilePath != expected {
		t.Errorf("expected HTTPFilePath=%s, got %s", expected, cfg.ConfigSection.HTTPFilePath)
	}
}

func TestLoad_HTTPFilePathStripsDirectory(t *testing.T) {
	// Even if httpFile contains a path, only the base filename should be used.
	dir := t.TempDir()
	yaml := `
config:
  httpFile: "../../etc/passwd"
  breaker_threshold_pct: 0.0

stages:
  - duration: 10s
    target_rps: 1
`
	cfgPath := writeTemp(t, dir, "config.yaml", yaml)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only the base name should be appended to the config dir.
	expected := filepath.Join(dir, "passwd")
	if cfg.ConfigSection.HTTPFilePath != expected {
		t.Errorf("expected traversal-safe path=%s, got %s", expected, cfg.ConfigSection.HTTPFilePath)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTemp(t, dir, "config.yaml", ":::invalid yaml:::")

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

// --- Validate tests ---

func TestValidate_MissingHTTPFile(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{
			HTTPFile:   "",
			BreakerPct: 10,
		},
		Stages: []Stage{
			{Duration: 10 * time.Second, TargetRPS: 50},
		},
	}
	err := cfg.Validate()
	if !errors.Is(err, ErrMissingHttpFile) {
		t.Errorf("expected ErrMissingHttpFile, got %v", err)
	}
}

func TestValidate_InvalidBreakerPct_Negative(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{
			HTTPFile:   "request.http",
			BreakerPct: -1,
		},
		Stages: []Stage{
			{Duration: 10 * time.Second, TargetRPS: 50},
		},
	}
	err := cfg.Validate()
	if !errors.Is(err, ErrInvalidBreakerThreshold) {
		t.Errorf("expected ErrInvalidBreakerThreshold, got %v", err)
	}
}

func TestValidate_InvalidBreakerPct_Over100(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{
			HTTPFile:   "request.http",
			BreakerPct: 101,
		},
		Stages: []Stage{
			{Duration: 10 * time.Second, TargetRPS: 50},
		},
	}
	err := cfg.Validate()
	if !errors.Is(err, ErrInvalidBreakerThreshold) {
		t.Errorf("expected ErrInvalidBreakerThreshold, got %v", err)
	}
}

func TestValidate_BreakerPctBoundaries(t *testing.T) {
	for _, pct := range []float64{0, 50, 100} {
		cfg := &Config{
			ConfigSection: Section{
				HTTPFile:   "request.http",
				BreakerPct: pct,
			},
			Stages: []Stage{
				{Duration: 10 * time.Second, TargetRPS: 50},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error for breaker_threshold_pct=%.0f, got %v", pct, err)
		}
	}
}

func TestValidate_NoStages(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{
			HTTPFile:   "request.http",
			BreakerPct: 10,
		},
		Stages: []Stage{},
	}
	err := cfg.Validate()
	if !errors.Is(err, ErrNoStagesDefined) {
		t.Errorf("expected ErrNoStagesDefined, got %v", err)
	}
}

func TestValidate_InvalidStageDuration(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{
			HTTPFile:   "request.http",
			BreakerPct: 10,
		},
		Stages: []Stage{
			{Duration: 0, TargetRPS: 50},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for zero duration, got nil")
	}
	expected := NewErrInvalidStageDuration(0)
	if err.Error() != expected.Error() {
		t.Errorf("expected %q, got %q", expected.Error(), err.Error())
	}
}

func TestValidate_NegativeStageDuration(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{
			HTTPFile:   "request.http",
			BreakerPct: 10,
		},
		Stages: []Stage{
			{Duration: -5 * time.Second, TargetRPS: 50},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative duration, got nil")
	}
}

func TestValidate_InvalidStageTargetRPS(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{
			HTTPFile:   "request.http",
			BreakerPct: 10,
		},
		Stages: []Stage{
			{Duration: 10 * time.Second, TargetRPS: 0},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for zero target_rps, got nil")
	}
	expected := NewErrInvalidStageTargetRPS(0)
	if err.Error() != expected.Error() {
		t.Errorf("expected %q, got %q", expected.Error(), err.Error())
	}
}

func TestValidate_NegativeStageTargetRPS(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{
			HTTPFile:   "request.http",
			BreakerPct: 10,
		},
		Stages: []Stage{
			{Duration: 10 * time.Second, TargetRPS: -10},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative target_rps, got nil")
	}
}

func TestValidate_MultipleStages_SecondInvalid(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{
			HTTPFile:   "request.http",
			BreakerPct: 10,
		},
		Stages: []Stage{
			{Duration: 10 * time.Second, TargetRPS: 50},
			{Duration: 0, TargetRPS: 100},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid second stage, got nil")
	}
	expected := NewErrInvalidStageDuration(1)
	if err.Error() != expected.Error() {
		t.Errorf("expected %q, got %q", expected.Error(), err.Error())
	}
}

func TestValidate_ValidMultipleStages(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{
			HTTPFile:   "request.http",
			BreakerPct: 20,
		},
		Stages: []Stage{
			{Duration: 10 * time.Second, TargetRPS: 50},
			{Duration: 30 * time.Second, TargetRPS: 100},
			{Duration: 1 * time.Minute, TargetRPS: 200},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error for valid multi-stage config, got %v", err)
	}
}
