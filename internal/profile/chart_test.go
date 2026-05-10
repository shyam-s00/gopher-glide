package profile_test

import (
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/profile"
)

// ── RenderASCIIChart ──────────────────────────────────────────────────────────

func TestRenderASCIIChart_EmptyStages(t *testing.T) {
	got := profile.RenderASCIIChart(nil)
	if got != "" {
		t.Errorf("expected empty string for nil stages, got %q", got)
	}

	got = profile.RenderASCIIChart([]config.Stage{})
	if got != "" {
		t.Errorf("expected empty string for empty stages, got %q", got)
	}
}

func TestRenderASCIIChart_ZeroRPSStages(t *testing.T) {
	stages := []config.Stage{
		{Duration: 30 * time.Second, TargetRPS: 0},
	}
	got := profile.RenderASCIIChart(stages)
	if got != "" {
		t.Errorf("expected empty string for zero-RPS stages, got %q", got)
	}
}

func TestRenderASCIIChart_HasExpectedLineCount(t *testing.T) {
	// 10 data rows + 1 border row + 1 time-label row = 12 lines.
	stages := []config.Stage{
		{Duration: 60 * time.Second, TargetRPS: 100},
	}
	got := profile.RenderASCIIChart(stages)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 12 {
		t.Errorf("expected 12 lines, got %d:\n%s", len(lines), got)
	}
}

func TestRenderASCIIChart_PeakLabelInFirstRow(t *testing.T) {
	stages := []config.Stage{
		{Duration: 60 * time.Second, TargetRPS: 500},
	}
	got := profile.RenderASCIIChart(stages)
	firstLine := strings.SplitN(got, "\n", 2)[0]
	if !strings.Contains(firstLine, "500") {
		t.Errorf("first row should contain peak label '500', got: %q", firstLine)
	}
}

func TestRenderASCIIChart_ZeroLabelInLastDataRow(t *testing.T) {
	stages := []config.Stage{
		{Duration: 60 * time.Second, TargetRPS: 200},
	}
	got := profile.RenderASCIIChart(stages)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	// Last data row is index 9 (0-based).
	lastDataRow := lines[9]
	if !strings.Contains(lastDataRow, "0") {
		t.Errorf("last data row should contain '0' label, got: %q", lastDataRow)
	}
}

func TestRenderASCIIChart_FlatFullPeak_AllColumnsFilledInFirstRow(t *testing.T) {
	// A flat profile at 100% peak: instant jump + hold so no lerp from 0.
	// Every column in every row should be filled.
	stages := []config.Stage{
		{Duration: 0, TargetRPS: 100},                // instant jump to peak
		{Duration: 60 * time.Second, TargetRPS: 100}, // hold at peak
	}
	got := profile.RenderASCIIChart(stages)
	lines := strings.Split(got, "\n")

	for i := 0; i < 10; i++ {
		// Strip the Y-axis prefix (everything before and including '│').
		parts := strings.SplitN(lines[i], "│", 2)
		if len(parts) < 2 {
			t.Fatalf("row %d has no '│' separator: %q", i, lines[i])
		}
		chartPart := parts[1]
		if strings.ContainsRune(chartPart, ' ') {
			t.Errorf("row %d has empty cell(s) at 100%% RPS: %q", i, chartPart)
		}
	}
}

func TestRenderASCIIChart_HalfPeak_TopHalfEmpty(t *testing.T) {
	// Instant jump to 50 RPS, hold full duration, then instant jump to 100 (peak setter).
	// The chart majority should be at ~50% height; the top rows should be mostly empty.
	stages := []config.Stage{
		{Duration: 0, TargetRPS: 50},                // instant jump to 50
		{Duration: 58 * time.Second, TargetRPS: 50}, // hold at 50 for most of the run
		{Duration: 0, TargetRPS: 100},               // instant jump to 100 at the very end (sets peak)
		{Duration: time.Second, TargetRPS: 100},     // 1s hold at peak (sets peak in peakRPS)
	}
	got := profile.RenderASCIIChart(stages)
	lines := strings.Split(got, "\n")

	// Top 4 rows (indices 0–3) represent RPS > 50% of peak.
	// With only the last 1/60th of columns at peak, those rows should have
	// mostly spaces (all but the last few columns empty).
	for i := 0; i <= 3; i++ {
		parts := strings.SplitN(lines[i], "│", 2)
		if len(parts) < 2 {
			continue
		}
		chartPart := parts[1]
		// More than half the columns should be empty.
		spaces := strings.Count(chartPart, " ")
		if spaces < len([]rune(chartPart))/2 {
			t.Errorf("row %d: expected mostly empty for <50%% RPS band, got: %q", i, chartPart)
		}
	}
}

func TestRenderASCIIChart_TimeAxisContainsLabels(t *testing.T) {
	stages := []config.Stage{
		{Duration: 3 * time.Minute, TargetRPS: 1000},
	}
	got := profile.RenderASCIIChart(stages)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	timeAxis := lines[len(lines)-1]

	if !strings.Contains(timeAxis, "0s") {
		t.Errorf("time axis missing start label '0s': %q", timeAxis)
	}
	if !strings.Contains(timeAxis, "3m") {
		t.Errorf("time axis missing end label '3m': %q", timeAxis)
	}
}

func TestRenderASCIIChart_InstantStages_AllColumnsFilledAtPeak(t *testing.T) {
	// All-instant stages → flat bar at peak.
	stages := []config.Stage{
		{Duration: 0, TargetRPS: 800},
	}
	got := profile.RenderASCIIChart(stages)
	if got == "" {
		t.Fatal("expected non-empty chart for all-instant stages")
	}
	// Top row should be completely filled.
	lines := strings.Split(got, "\n")
	parts := strings.SplitN(lines[0], "│", 2)
	if len(parts) < 2 {
		t.Fatalf("first row has no '│' separator: %q", lines[0])
	}
	if strings.ContainsRune(parts[1], ' ') {
		t.Errorf("top row should be fully filled for instant-peak stages, got: %q", parts[1])
	}
}

func TestRenderASCIIChart_RampUp_TopRowSparselyCovered(t *testing.T) {
	// A linear ramp from 0 to 100 means only the very end reaches peak.
	// The first row (peak band) should have spaces in the early columns
	// and blocks only near the right side.
	rampStages := []config.Stage{
		{Duration: 60 * time.Second, TargetRPS: 0},   // ramp from 0 → 0 (flat zero)
		{Duration: 60 * time.Second, TargetRPS: 100}, // ramp 0 → 100
	}
	got := profile.RenderASCIIChart(rampStages)
	lines := strings.Split(got, "\n")
	parts := strings.SplitN(lines[0], "│", 2)
	if len(parts) < 2 {
		t.Fatalf("first row has no '│' separator: %q", lines[0])
	}
	// The first half of the top row should be empty.
	firstHalf := parts[1][:len(parts[1])/2]
	if !strings.ContainsRune(firstHalf, ' ') {
		t.Errorf("expected spaces in first half of top row for ramp profile, got: %q", firstHalf)
	}
}

func TestRenderASCIIChart_RealProfile_FlashSale(t *testing.T) {
	prof, err := profile.Load("flash-sale")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stages := profile.InflateSegments(prof, prof.DefaultPeakRPS, prof.DefaultDuration)
	got := profile.RenderASCIIChart(stages)
	if got == "" {
		t.Fatal("expected non-empty chart for flash-sale profile")
	}
	if !strings.Contains(got, "0s") {
		t.Error("chart missing start time label '0s'")
	}
}

func TestRenderASCIIChart_ChartWidthIs60Columns(t *testing.T) {
	stages := []config.Stage{
		{Duration: 60 * time.Second, TargetRPS: 100},
	}
	got := profile.RenderASCIIChart(stages)
	lines := strings.Split(got, "\n")
	for i := 0; i < 10; i++ {
		parts := strings.SplitN(lines[i], "│", 2)
		if len(parts) < 2 {
			t.Fatalf("row %d has no separator", i)
		}
		if len([]rune(parts[1])) != 60 {
			t.Errorf("row %d: chart body width = %d, want 60", i, len([]rune(parts[1])))
		}
	}
}
