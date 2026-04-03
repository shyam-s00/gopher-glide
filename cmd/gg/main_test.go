package main

import (
	"testing"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// ── snapDisplayTag ────────────────────────────────────────────────────────────

func TestSnapDisplayTag(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", "(untagged)"},
		{"run", "(untagged)"},
		{"v1.0.0", "v1.0.0"},
		{"my-baseline", "my-baseline"},
	}
	for _, c := range cases {
		got := snapDisplayTag(c.input)
		if got != c.want {
			t.Errorf("snapDisplayTag(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// ── resolveDisplayMaxSamples ─────────────────────────────────────────────────

func TestResolveDisplayMaxSamples_Zero(t *testing.T) {
	got := resolveDisplayMaxSamples(0)
	if got != snap.DefaultMaxBodySamples {
		t.Errorf("resolveDisplayMaxSamples(0) = %d, want %d", got, snap.DefaultMaxBodySamples)
	}
}

func TestResolveDisplayMaxSamples_Negative(t *testing.T) {
	got := resolveDisplayMaxSamples(-10)
	if got != snap.DefaultMaxBodySamples {
		t.Errorf("resolveDisplayMaxSamples(-10) = %d, want %d", got, snap.DefaultMaxBodySamples)
	}
}

func TestResolveDisplayMaxSamples_Positive(t *testing.T) {
	got := resolveDisplayMaxSamples(50)
	if got != 50 {
		t.Errorf("resolveDisplayMaxSamples(50) = %d, want 50", got)
	}
}

// ── snapFormatCount ───────────────────────────────────────────────────────────

func TestSnapFormatCount(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{9999, "9,999"},
		{999999, "999,999"},
		{1000000, "1,000,000"},
		{1500000, "1,500,000"},
		{9999999, "9,999,999"},
	}
	for _, c := range cases {
		got := snapFormatCount(c.n)
		if got != c.want {
			t.Errorf("snapFormatCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
