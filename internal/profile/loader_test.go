package profile_test

import (
	"errors"
	"testing"

	"github.com/shyam-s00/gopher-glide/internal/profile"
)

func TestListNames_Returns21Profiles(t *testing.T) {
	names := profile.ListNames()
	if len(names) != 21 {
		t.Errorf("expected 21 embedded profiles, got %d: %v", len(names), names)
	}
}

func TestListNames_ContainsExpectedProfiles(t *testing.T) {
	want := []string{
		"flash-sale", "black-friday", "ticket-release", "inventory-drop",
		"canary", "smoke", "load", "stress", "soak", "endurance",
		"ddos", "spike", "burst", "retry-storm", "chaos",
		"step-up", "wave", "scale-down", "crawler", "trickle", "warm-up",
	}
	nameSet := make(map[string]bool, 21)
	for _, n := range profile.ListNames() {
		nameSet[n] = true
	}
	for _, w := range want {
		if !nameSet[w] {
			t.Errorf("expected profile %q in embedded list", w)
		}
	}
}

func TestLoad_EmbeddedProfile(t *testing.T) {
	p, err := profile.Load("smoke")
	if err != nil {
		t.Fatalf("Load(smoke): unexpected error: %v", err)
	}
	if p.Name != "smoke" {
		t.Errorf("Name: got %q, want smoke", p.Name)
	}
	if p.DefaultPeakRPS != 10 {
		t.Errorf("DefaultPeakRPS: got %d, want 10", p.DefaultPeakRPS)
	}
	if len(p.Segments) == 0 {
		t.Error("Segments must not be empty")
	}
}

func TestLoad_AllEmbeddedProfilesParseCleanly(t *testing.T) {
	for _, name := range profile.ListNames() {
		t.Run(name, func(t *testing.T) {
			p, err := profile.Load(name)
			if err != nil {
				t.Fatalf("Load(%q): %v", name, err)
			}
			if p.DefaultDuration <= 0 {
				t.Errorf("DefaultDuration must be > 0")
			}
			if p.DefaultPeakRPS <= 0 {
				t.Errorf("DefaultPeakRPS must be > 0")
			}
			if len(p.Segments) == 0 {
				t.Errorf("Segments must not be empty")
			}
			total := p.TotalNonZeroPct()
			if total < 0.99 || total > 1.01 {
				t.Errorf("non-zero duration_pct sum = %.4f, want ~1.0", total)
			}
		})
	}
}

func TestLoad_UnknownProfile_ReturnsErrNotFound(t *testing.T) {
	_, err := profile.Load("does-not-exist")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, profile.ErrProfileNotFound) {
		t.Errorf("expected ErrProfileNotFound, got: %v", err)
	}
}

func TestLoad_FlashSale(t *testing.T) {
	p, err := profile.Load("flash-sale")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.DefaultPeakRPS != 2000 {
		t.Errorf("DefaultPeakRPS: got %d, want 2000", p.DefaultPeakRPS)
	}
	if p.Segments[0].Type != profile.SegmentStep {
		t.Errorf("Segments[0].Type: got %q, want step", p.Segments[0].Type)
	}
	if p.Segments[0].RPSMultiplier != 1.0 {
		t.Errorf("Segments[0].RPSMultiplier: got %g, want 1.0", p.Segments[0].RPSMultiplier)
	}
}

func TestLoad_Chaos_HasJitterOverride(t *testing.T) {
	p, err := profile.Load("chaos")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ConfigOverride.Jitter != 0.4 {
		t.Errorf("ConfigOverride.Jitter: got %g, want 0.4", p.ConfigOverride.Jitter)
	}
}

func TestLoad_LoadProfile_HasThreeSegments(t *testing.T) {
	p, err := profile.Load("load")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Segments) != 3 {
		t.Fatalf("len(Segments): got %d, want 3", len(p.Segments))
	}
	expected := [3]profile.SegmentType{
		profile.SegmentLinear,
		profile.SegmentFlat,
		profile.SegmentLinear,
	}
	for i, want := range expected {
		if p.Segments[i].Type != want {
			t.Errorf("Segments[%d].Type: got %q, want %q", i, p.Segments[i].Type, want)
		}
	}
}
