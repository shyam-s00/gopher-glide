package config

import (
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

// ── Load ──────────────────────────────────────────────────────────────────────

func TestLoad_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	yaml := `
config:
  httpFile: "request.http"
  prometheus: true
  prometheus_port: 8080
  breaker_threshold_pct: 10.0
  jitter: 0.1
  time_scale: 2.0

stages:
  - name: "Ramp Up"
    duration: 30s
    target_rps: 100
  - name: "Sustain"
    duration: 1m
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
	if cfg.ConfigSection.Jitter != 0.1 {
		t.Errorf("expected jitter=0.1, got %f", cfg.ConfigSection.Jitter)
	}
	if cfg.ConfigSection.TimeScale != 2.0 {
		t.Errorf("expected time_scale=2.0, got %f", cfg.ConfigSection.TimeScale)
	}
	if len(cfg.Stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(cfg.Stages))
	}
	if cfg.Stages[0].Name != "Ramp Up" {
		t.Errorf("expected stage[0].name=Ramp Up, got %s", cfg.Stages[0].Name)
	}
	if cfg.Stages[0].Duration != 30*time.Second {
		t.Errorf("expected stage[0].duration=30s, got %v", cfg.Stages[0].Duration)
	}
	if cfg.Stages[0].TargetRPS != 100 {
		t.Errorf("expected stage[0].target_rps=100, got %d", cfg.Stages[0].TargetRPS)
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

// ── Validate — Section fields ─────────────────────────────────────────────────

func TestValidate_MissingHTTPFile(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{BreakerPct: 10},
		Stages:        []Stage{{Duration: 10 * time.Second, TargetRPS: 50}},
	}
	if err := cfg.Validate(); err != ErrMissingHttpFile {
		t.Errorf("expected ErrMissingHttpFile, got %v", err)
	}
}

func TestValidate_InvalidBreakerPct_Negative(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{HTTPFile: "f.http", BreakerPct: -1},
		Stages:        []Stage{{Duration: 10 * time.Second, TargetRPS: 50}},
	}
	if err := cfg.Validate(); err != ErrInvalidBreakerThreshold {
		t.Errorf("expected ErrInvalidBreakerThreshold, got %v", err)
	}
}

func TestValidate_InvalidBreakerPct_Over100(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{HTTPFile: "f.http", BreakerPct: 101},
		Stages:        []Stage{{Duration: 10 * time.Second, TargetRPS: 50}},
	}
	if err := cfg.Validate(); err != ErrInvalidBreakerThreshold {
		t.Errorf("expected ErrInvalidBreakerThreshold, got %v", err)
	}
}

func TestValidate_BreakerPctBoundaries(t *testing.T) {
	for _, pct := range []float64{0, 50, 100} {
		cfg := &Config{
			ConfigSection: Section{HTTPFile: "f.http", BreakerPct: pct},
			Stages:        []Stage{{Duration: 10 * time.Second, TargetRPS: 50}},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error for breaker_threshold_pct=%.0f, got %v", pct, err)
		}
	}
}

func TestValidate_InvalidJitter_Negative(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{HTTPFile: "f.http", Jitter: -0.1},
		Stages:        []Stage{{Duration: 10 * time.Second, TargetRPS: 50}},
	}
	if err := cfg.Validate(); err != ErrInvalidJitter {
		t.Errorf("expected ErrInvalidJitter, got %v", err)
	}
}

func TestValidate_InvalidJitter_OverOne(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{HTTPFile: "f.http", Jitter: 1.1},
		Stages:        []Stage{{Duration: 10 * time.Second, TargetRPS: 50}},
	}
	if err := cfg.Validate(); err != ErrInvalidJitter {
		t.Errorf("expected ErrInvalidJitter, got %v", err)
	}
}

func TestValidate_JitterBoundaries(t *testing.T) {
	for _, j := range []float64{0, 0.5, 1.0} {
		cfg := &Config{
			ConfigSection: Section{HTTPFile: "f.http", Jitter: j},
			Stages:        []Stage{{Duration: 10 * time.Second, TargetRPS: 50}},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error for jitter=%.1f, got %v", j, err)
		}
	}
}

func TestValidate_InvalidTimeScale_Negative(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{HTTPFile: "f.http", TimeScale: -1},
		Stages:        []Stage{{Duration: 10 * time.Second, TargetRPS: 50}},
	}
	if err := cfg.Validate(); err != ErrInvalidTimeScale {
		t.Errorf("expected ErrInvalidTimeScale, got %v", err)
	}
}

func TestValidate_TimeScaleZeroIsValid(t *testing.T) {
	// 0 is treated as "not set" — falls back to 1.0 at runtime
	cfg := &Config{
		ConfigSection: Section{HTTPFile: "f.http", TimeScale: 0},
		Stages:        []Stage{{Duration: 10 * time.Second, TargetRPS: 50}},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error for time_scale=0, got %v", err)
	}
}

// ── Validate — Stages ─────────────────────────────────────────────────────────

func TestValidate_NoStages(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{HTTPFile: "f.http"},
		Stages:        []Stage{},
	}
	if err := cfg.Validate(); err != ErrNoStagesDefined {
		t.Errorf("expected ErrNoStagesDefined, got %v", err)
	}
}

func TestValidate_StageDurationZero_IsValidSpike(t *testing.T) {
	// duration: 0 is explicitly allowed for instant spike steps
	cfg := &Config{
		ConfigSection: Section{HTTPFile: "f.http"},
		Stages:        []Stage{{Duration: 0, TargetRPS: 200}},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error for spike stage (duration=0), got %v", err)
	}
}

func TestValidate_StageDurationNegative_IsInvalid(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{HTTPFile: "f.http"},
		Stages:        []Stage{{Duration: -1 * time.Second, TargetRPS: 50}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative duration, got nil")
	}
	expected := NewErrInvalidStageDuration(0)
	if err.Error() != expected.Error() {
		t.Errorf("expected %q, got %q", expected.Error(), err.Error())
	}
}

func TestValidate_StageTargetRPSZero_IsValidCoolDown(t *testing.T) {
	// target_rps: 0 is explicitly allowed for cool-down stages
	cfg := &Config{
		ConfigSection: Section{HTTPFile: "f.http"},
		Stages:        []Stage{{Duration: 30 * time.Second, TargetRPS: 0}},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error for cool-down stage (target_rps=0), got %v", err)
	}
}

func TestValidate_StageTargetRPSNegative_IsInvalid(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{HTTPFile: "f.http"},
		Stages:        []Stage{{Duration: 10 * time.Second, TargetRPS: -1}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative target_rps, got nil")
	}
	expected := NewErrInvalidStageTargetRPS(0)
	if err.Error() != expected.Error() {
		t.Errorf("expected %q, got %q", expected.Error(), err.Error())
	}
}

func TestValidate_MultipleStages_SecondInvalid(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{HTTPFile: "f.http"},
		Stages: []Stage{
			{Duration: 10 * time.Second, TargetRPS: 50},
			{Duration: -1 * time.Second, TargetRPS: 100},
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

func TestValidate_FullMultiStageConfig(t *testing.T) {
	// mirrors the reference config.yaml — all four profiles
	cfg := &Config{
		ConfigSection: Section{
			HTTPFile:   "request.http",
			BreakerPct: 20,
			Jitter:     0.1,
			TimeScale:  1.0,
		},
		Stages: []Stage{
			{Name: "Ramp Up", Duration: 30 * time.Second, TargetRPS: 50},
			{Name: "Sustain", Duration: 1 * time.Minute, TargetRPS: 50},
			{Name: "Spike", Duration: 0, TargetRPS: 200},
			{Name: "Hold Spike", Duration: 10 * time.Second, TargetRPS: 200},
			{Name: "Ramp Down", Duration: 30 * time.Second, TargetRPS: 0},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error for full multi-stage config, got %v", err)
	}
}

// ── Stage.Label ───────────────────────────────────────────────────────────────

func TestStageLabel_UsesNameWhenSet(t *testing.T) {
	s := Stage{Name: "My Custom Stage", Duration: 30 * time.Second, TargetRPS: 50}
	if got := s.Label(0); got != "My Custom Stage" {
		t.Errorf("expected custom name, got %q", got)
	}
}

func TestStageLabel_InferRampUp(t *testing.T) {
	s := Stage{Duration: 30 * time.Second, TargetRPS: 100}
	if got := s.Label(50); got != "Ramp Up" {
		t.Errorf("expected Ramp Up, got %q", got)
	}
}

func TestStageLabel_InferSustain(t *testing.T) {
	s := Stage{Duration: 1 * time.Minute, TargetRPS: 100}
	if got := s.Label(100); got != "Sustain" {
		t.Errorf("expected Sustain, got %q", got)
	}
}

func TestStageLabel_InferSpike(t *testing.T) {
	s := Stage{Duration: 0, TargetRPS: 200}
	if got := s.Label(50); got != "Spike" {
		t.Errorf("expected Spike, got %q", got)
	}
}

func TestStageLabel_InferRampDown(t *testing.T) {
	s := Stage{Duration: 30 * time.Second, TargetRPS: 0}
	if got := s.Label(200); got != "Ramp Down" {
		t.Errorf("expected Ramp Down, got %q", got)
	}
}

func TestStageLabel_InferRampDownPartial(t *testing.T) {
	s := Stage{Duration: 30 * time.Second, TargetRPS: 30}
	if got := s.Label(100); got != "Ramp Down" {
		t.Errorf("expected Ramp Down for decreasing target, got %q", got)
	}
}

// ── Config helpers ─────────────────────────────────────────────────────────────

func TestTotalDuration_RealTime(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{TimeScale: 1.0},
		Stages: []Stage{
			{Duration: 30 * time.Second},
			{Duration: 1 * time.Minute},
			{Duration: 0},
			{Duration: 10 * time.Second},
			{Duration: 30 * time.Second},
		},
	}
	// 30s + 60s + 0s + 10s + 30s = 130s
	expected := 130 * time.Second
	if got := cfg.TotalDuration(); got != expected {
		t.Errorf("expected TotalDuration=%v, got %v", expected, got)
	}
}

func TestTotalDuration_WarpSpeed(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{TimeScale: 2.0},
		Stages: []Stage{
			{Duration: 60 * time.Second},
			{Duration: 60 * time.Second},
		},
	}
	// 120s total / 2.0 scale = 60s effective
	expected := 60 * time.Second
	if got := cfg.TotalDuration(); got != expected {
		t.Errorf("expected TotalDuration=%v at 2x warp, got %v", expected, got)
	}
}

func TestTotalDuration_ZeroScaleFallsBackToRealTime(t *testing.T) {
	cfg := &Config{
		ConfigSection: Section{TimeScale: 0},
		Stages:        []Stage{{Duration: 60 * time.Second}},
	}
	if got := cfg.TotalDuration(); got != 60*time.Second {
		t.Errorf("expected 60s for zero time_scale fallback, got %v", got)
	}
}

func TestPeakRPS(t *testing.T) {
	cfg := &Config{
		Stages: []Stage{
			{TargetRPS: 50},
			{TargetRPS: 200},
			{TargetRPS: 100},
			{TargetRPS: 0},
		},
	}
	if got := cfg.PeakRPS(); got != 200 {
		t.Errorf("expected PeakRPS=200, got %d", got)
	}
}

func TestPeakRPS_SingleStage(t *testing.T) {
	cfg := &Config{
		Stages: []Stage{{TargetRPS: 75}},
	}
	if got := cfg.PeakRPS(); got != 75 {
		t.Errorf("expected PeakRPS=75, got %d", got)
	}
}
