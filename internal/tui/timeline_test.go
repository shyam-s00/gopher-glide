package tui

import (
	"gopher-glide/internal/config"
	"testing"
	"time"
)

// rpsAtForTest mirrors the rpsAt step-function closure in renderTimeline exactly.
// It must be kept in sync with the production implementation.
func rpsAtForTest(stages []config.Stage, t time.Duration) float64 {
	acc := time.Duration(0)
	prev := 0.0
	for _, s := range stages {
		if s.Duration == 0 {
			prev = float64(s.TargetRPS)
			continue
		}
		end := acc + s.Duration
		if t < end {
			return float64(s.TargetRPS)
		}
		acc = end
		prev = float64(s.TargetRPS)
	}
	return prev
}

func TestRpsAt_Stages(t *testing.T) {
	stages := []config.Stage{
		{Duration: 10 * time.Second, TargetRPS: 75},  // step  → 75  for t in [0,10s)
		{Duration: 60 * time.Second, TargetRPS: 50},  // step  → 50  for t in [10s,70s)
		{Duration: 0, TargetRPS: 200},                // spike → 200 instantly
		{Duration: 10 * time.Second, TargetRPS: 200}, // step  → 200 for t in [70s,80s)
		{Duration: 30 * time.Second, TargetRPS: 0},   // step  → 0   for t in [80s,110s)
	}

	cases := []struct {
		label string
		t     time.Duration
		want  float64
	}{
		// Stage 1: returns 75 for any t in [0, 10s)
		{"start of stage 1", 0, 75},
		{"mid stage 1", 5 * time.Second, 75},
		{"end of stage 1 (boundary)", 10 * time.Second, 50}, // t==10s enters stage 2

		// Stage 2: returns 50 for t in [10s, 70s)
		{"start of stage 2", 11 * time.Second, 50},
		{"mid stage 2", 40 * time.Second, 50},
		{"end of stage 2 (boundary)", 70 * time.Second, 200}, // spike absorbed; t==70s enters stage 4

		// Spike (duration 0): prev advances to 200, no range consumed
		{"1ns after spike", 70*time.Second + 1, 200},

		// Stage 4: returns 200 for t in [70s, 80s)
		{"mid stage 4", 75 * time.Second, 200},
		{"end of stage 4 (boundary)", 80 * time.Second, 0}, // t==80s enters stage 5

		// Stage 5: returns 0 for t in [80s, 110s)
		{"mid stage 5", 95 * time.Second, 0},
		{"end of stage 5", 110 * time.Second, 0}, // past end → returns last prev
	}

	for _, c := range cases {
		got := rpsAtForTest(stages, c.t)
		t.Logf("%-35s t=%-10s  want=%-7.2f  got=%.2f", c.label, c.t, c.want, got)
		if abs(got-c.want) > 0.01 {
			t.Errorf("%s: want %.2f got %.2f", c.label, c.want, got)
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
