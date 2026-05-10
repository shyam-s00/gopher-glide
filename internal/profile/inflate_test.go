package profile_test

import (
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/profile"
)

// ── InflateSegments: step ─────────────────────────────────────────────────────

func TestInflate_Step_InstantJumpAndHold(t *testing.T) {
	prof := profileWith([]profile.Segment{
		{Type: profile.SegmentStep, DurationPct: 1.0, RPSMultiplier: 1.0},
	}, 1000, 60*time.Second)

	stages := profile.InflateSegments(prof, 1000, 60*time.Second)

	// Expect: 0-dur instant jump + 60s hold
	if len(stages) != 2 {
		t.Fatalf("len(stages) = %d, want 2", len(stages))
	}
	if stages[0].Duration != 0 {
		t.Errorf("stages[0].Duration = %v, want 0", stages[0].Duration)
	}
	if stages[0].TargetRPS != 1000 {
		t.Errorf("stages[0].TargetRPS = %d, want 1000", stages[0].TargetRPS)
	}
	if stages[1].Duration != 60*time.Second {
		t.Errorf("stages[1].Duration = %v, want 60s", stages[1].Duration)
	}
	if stages[1].TargetRPS != 1000 {
		t.Errorf("stages[1].TargetRPS = %d, want 1000", stages[1].TargetRPS)
	}
}

func TestInflate_Step_ZeroDuration_PureInstant(t *testing.T) {
	prof := profileWith([]profile.Segment{
		{Type: profile.SegmentStep, DurationPct: 1.0, RPSMultiplier: 1.0},
		{Type: profile.SegmentStep, DurationPct: 0.0, RPSMultiplier: 0.0}, // instant drop
	}, 1000, 60*time.Second)

	stages := profile.InflateSegments(prof, 1000, 60*time.Second)
	// [0-dur jump to 1000] [60s hold at 1000] [0-dur drop to 0]
	if len(stages) != 3 {
		t.Fatalf("len(stages) = %d, want 3", len(stages))
	}
	last := stages[len(stages)-1]
	if last.Duration != 0 {
		t.Errorf("last stage Duration = %v, want 0 (instant drop)", last.Duration)
	}
	if last.TargetRPS != 0 {
		t.Errorf("last stage TargetRPS = %d, want 0", last.TargetRPS)
	}
}

// ── InflateSegments: flat ─────────────────────────────────────────────────────

func TestInflate_Flat_SnapAndHold_WhenPrevDiffers(t *testing.T) {
	// Starting from 0, a flat segment at 100% peak should snap first then hold.
	prof := profileWith([]profile.Segment{
		{Type: profile.SegmentFlat, DurationPct: 1.0, RPSMultiplier: 1.0},
	}, 500, 30*time.Second)

	stages := profile.InflateSegments(prof, 500, 30*time.Second)
	// prevRPS=0, target=500 → 0-dur snap + 30s hold
	if len(stages) != 2 {
		t.Fatalf("len(stages) = %d, want 2 (snap + hold)", len(stages))
	}
	if stages[0].Duration != 0 {
		t.Errorf("snap stage Duration = %v, want 0", stages[0].Duration)
	}
	if stages[1].Duration != 30*time.Second {
		t.Errorf("hold stage Duration = %v, want 30s", stages[1].Duration)
	}
}

func TestInflate_Flat_NoSnap_WhenPrevSameLevel(t *testing.T) {
	// linear ramp up to peak → flat sustain: no snap needed since prev ends at same level.
	prof := profileWith([]profile.Segment{
		{Type: profile.SegmentLinear, DurationPct: 0.5, RPSMultiplier: 1.0},
		{Type: profile.SegmentFlat, DurationPct: 0.5, RPSMultiplier: 1.0},
	}, 200, 60*time.Second)

	stages := profile.InflateSegments(prof, 200, 60*time.Second)
	// linear stage + flat hold (no extra snap)
	if len(stages) != 2 {
		t.Fatalf("len(stages) = %d, want 2 (linear + flat hold, no extra snap)", len(stages))
	}
}

// ── InflateSegments: linear ───────────────────────────────────────────────────

func TestInflate_Linear_SingleStage(t *testing.T) {
	prof := profileWith([]profile.Segment{
		{Type: profile.SegmentLinear, DurationPct: 0.25, RPSMultiplier: 1.0},
		{Type: profile.SegmentFlat, DurationPct: 0.50, RPSMultiplier: 1.0},
		{Type: profile.SegmentLinear, DurationPct: 0.25, RPSMultiplier: 0.0},
	}, 100, 2*time.Minute)

	stages := profile.InflateSegments(prof, 100, 2*time.Minute)
	// linear up + flat hold (no snap; prev ends at peak) + linear down
	if len(stages) != 3 {
		t.Fatalf("len(stages) = %d, want 3", len(stages))
	}
	if stages[0].TargetRPS != 100 {
		t.Errorf("ramp up TargetRPS = %d, want 100", stages[0].TargetRPS)
	}
	if stages[0].Duration != 30*time.Second {
		t.Errorf("ramp up Duration = %v, want 30s", stages[0].Duration)
	}
	if stages[2].TargetRPS != 0 {
		t.Errorf("ramp down TargetRPS = %d, want 0", stages[2].TargetRPS)
	}
}

// ── InflateSegments: exponential ─────────────────────────────────────────────

func TestInflate_Exponential_ProducesMultipleSubStages(t *testing.T) {
	prof := profileWith([]profile.Segment{
		{Type: profile.SegmentExponential, DurationPct: 1.0, RPSMultiplier: 1.0},
	}, 1000, 80*time.Second)

	stages := profile.InflateSegments(prof, 1000, 80*time.Second)
	// Should produce expSubSteps (8) sub-stages.
	if len(stages) != 8 {
		t.Fatalf("len(stages) = %d, want 8", len(stages))
	}
	// Last sub-stage should end at peak.
	last := stages[len(stages)-1]
	if last.TargetRPS != 1000 {
		t.Errorf("last sub-stage TargetRPS = %d, want 1000", last.TargetRPS)
	}
	// Sub-stages should be strictly increasing (exponential ramp from 0).
	for i := 1; i < len(stages); i++ {
		if stages[i].TargetRPS <= stages[i-1].TargetRPS {
			t.Errorf("stages[%d].TargetRPS (%d) <= stages[%d].TargetRPS (%d): not monotonically increasing",
				i, stages[i].TargetRPS, i-1, stages[i-1].TargetRPS)
		}
	}
}

func TestInflate_Exponential_EqualSubDurations(t *testing.T) {
	prof := profileWith([]profile.Segment{
		{Type: profile.SegmentExponential, DurationPct: 1.0, RPSMultiplier: 1.0},
	}, 500, 80*time.Second)

	stages := profile.InflateSegments(prof, 500, 80*time.Second)
	wantSubDur := 80 * time.Second / 8
	for i, s := range stages {
		if s.Duration != wantSubDur {
			t.Errorf("stages[%d].Duration = %v, want %v", i, s.Duration, wantSubDur)
		}
	}
}

// ── InflateSegments: flash-sale integration ───────────────────────────────────

func TestInflate_FlashSaleProfile(t *testing.T) {
	prof, err := profile.Load("flash-sale")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	stages := profile.InflateSegments(prof, prof.DefaultPeakRPS, prof.DefaultDuration)
	if len(stages) == 0 {
		t.Fatal("expected at least one stage")
	}
	// First stage must be an instant jump to peak.
	if stages[0].Duration != 0 {
		t.Errorf("stages[0].Duration = %v, want 0 (instant jump)", stages[0].Duration)
	}
	if stages[0].TargetRPS != prof.DefaultPeakRPS {
		t.Errorf("stages[0].TargetRPS = %d, want %d", stages[0].TargetRPS, prof.DefaultPeakRPS)
	}
	// Last stage must be an instant drop to 0.
	last := stages[len(stages)-1]
	if last.Duration != 0 {
		t.Errorf("last stage Duration = %v, want 0 (instant drop)", last.Duration)
	}
	if last.TargetRPS != 0 {
		t.Errorf("last stage TargetRPS = %d, want 0", last.TargetRPS)
	}
}

// ── InflateSegments: scale to custom peak / duration ─────────────────────────

func TestInflate_CustomPeakAndDuration(t *testing.T) {
	prof, err := profile.Load("load")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Override: half peak, double duration.
	customPeak := prof.DefaultPeakRPS / 2
	customDur := prof.DefaultDuration * 2

	stages := profile.InflateSegments(prof, customPeak, customDur)
	if len(stages) == 0 {
		t.Fatal("expected stages")
	}

	// Find peak RPS in inflated stages.
	maxRPS := 0
	var totalDur time.Duration
	for _, s := range stages {
		if s.TargetRPS > maxRPS {
			maxRPS = s.TargetRPS
		}
		totalDur += s.Duration
	}
	if maxRPS != customPeak {
		t.Errorf("inflated peak RPS = %d, want %d", maxRPS, customPeak)
	}
	if totalDur != customDur {
		t.Errorf("inflated total duration = %v, want %v", totalDur, customDur)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// profileWith builds a minimal *Profile for use in inflate tests, without
// going through the YAML loader.
func profileWith(segs []profile.Segment, peakRPS int, dur time.Duration) *profile.Profile {
	return &profile.Profile{
		Name:            "test",
		DefaultPeakRPS:  peakRPS,
		DefaultDuration: dur,
		Segments:        segs,
	}
}
