package tui

import (
	"fmt"
	"gopher-glide/internal/config"
	"testing"
	"time"
)

// rpsAtForTest is a copy of the rpsAt closure from renderTimeline,
// extracted so we can unit-test the curve logic directly.
func rpsAtForTest(stages []config.Stage, t time.Duration) float64 {
	cur := time.Duration(0)
	prev := 0.0
	for _, s := range stages {
		if s.Duration == 0 {
			if t <= cur {
				return prev
			}
			prev = float64(s.TargetRPS)
			continue
		}
		end := cur + s.Duration
		if t <= end {
			p := float64(t-cur) / float64(s.Duration)
			return prev + (float64(s.TargetRPS)-prev)*p
		}
		cur = end
		prev = float64(s.TargetRPS)
	}
	return prev
}

func TestRpsAt_Stages(t *testing.T) {
	stages := []config.Stage{
		{Duration: 10 * time.Second, TargetRPS: 75},  // ramp  0→75
		{Duration: 60 * time.Second, TargetRPS: 50},  // ramp  75→50 (actually ramps DOWN)
		{Duration: 0, TargetRPS: 200},                // spike 50→200 instantly
		{Duration: 10 * time.Second, TargetRPS: 200}, // hold  200
		{Duration: 30 * time.Second, TargetRPS: 0},   // ramp  200→0
	}

	cases := []struct {
		label string
		t     time.Duration
		want  float64
	}{
		{"start", 0, 0},
		{"mid ramp-up", 5 * time.Second, 37.5},
		{"end ramp-up", 10 * time.Second, 75},
		{"1s into sustain", 11 * time.Second, 75 - (25.0 / 60)},
		{"mid sustain", 40 * time.Second, 75 - 25.0*(30.0/60)},
		{"end sustain", 70 * time.Second, 50},
		{"1ns after spike", 70*time.Second + 1, 200},
		{"mid hold", 75 * time.Second, 200},
		{"end hold", 80 * time.Second, 200},
		{"mid ramp-down", 95 * time.Second, 100},
		{"end ramp-down", 110 * time.Second, 0},
	}

	for _, c := range cases {
		got := rpsAtForTest(stages, c.t)
		fmt.Printf("%-30s t=%-6s  want=%-7.2f  got=%.2f\n", c.label, c.t, c.want, got)
		if abs(got-c.want) > 0.1 {
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
