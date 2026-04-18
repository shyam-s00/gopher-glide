package main

import (
	"os"
	"path/filepath"
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

// ── resolveSnapTarget ────────────────────────────────────────────────────────

func TestResolveSnapTarget(t *testing.T) {
	dir := t.TempDir()

	// Create dummy snapshot files
	// snap.List processes files sorted by Date, extracted from suffix.
	f1 := filepath.Join(dir, "run-20230101-100000.snap")
	f2 := filepath.Join(dir, "tagA-20230101-110000.snap")
	_ = os.WriteFile(f1, []byte("{}"), 0o644)
	_ = os.WriteFile(f2, []byte("{}"), 0o644)

	// Test 1: By Numeric ID
	// Because f1 is chronologically first, it gets ID=1.
	info, err := resolveSnapTarget("1", dir)
	if err != nil {
		t.Fatalf("expected to resolve numeric ID 1: %v", err)
	}
	if info.FileName != "run-20230101-100000.snap" {
		t.Errorf("expected ID 1 to match f1, got %s", info.FileName)
	}

	// Test 2: By Existing File Path
	// Provide the actual absolute path to f2
	info, err = resolveSnapTarget(f2, dir)
	if err != nil {
		t.Fatalf("expected to resolve exact file path: %v", err)
	}
	if info.Path != f2 {
		t.Errorf("expected Path to be %s, got %s", f2, info.Path)
	}

	// Test 3: By Tag Name
	info, err = resolveSnapTarget("tagA", dir)
	if err != nil {
		t.Fatalf("expected to resolve tag 'tagA': %v", err)
	}
	if info.FileName != "tagA-20230101-110000.snap" {
		t.Errorf("expected tagA to match f2, got %s", info.FileName)
	}

	// Test 4: Failure numeric ID
	_, err = resolveSnapTarget("99", dir)
	if err == nil {
		t.Error("expected error for non-existent numeric ID")
	}

	// Test 5: Failure tag
	_, err = resolveSnapTarget("nonexistentTag", dir)
	if err == nil {
		t.Error("expected error for non-existent tag")
	}

	// Test 6: Precedence - A purely numeric file name exists locally, but we treat numeric target as ID.
	fNumeric := filepath.Join(dir, "999") // no .snap extension needed for stat
	_ = os.WriteFile(fNumeric, []byte{}, 0o644)
	originalWd, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer os.Chdir(originalWd) // ensure we go back safely

	// Calling target="999". This parses as integer.
	// We expect an error because FindByID(999) fails and does NOT fall through to os.Stat!
	_, err = resolveSnapTarget("999", dir)
	if err == nil {
		t.Error("expected numeric string to exclusively trigger ID lookup and NOT fall through to os.Stat even if a file named '999' exists")
	}
}
