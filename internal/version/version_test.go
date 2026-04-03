package version

import (
	"strings"
	"testing"
	"time"
)

func TestGetBuildDate_Stamped(t *testing.T) {
	// When BuildDate is set to a valid RFC3339 string, GetBuildDate returns it verbatim.
	original := BuildDate
	defer func() { BuildDate = original }()

	BuildDate = "2026-01-15T12:00:00Z"
	got := GetBuildDate()
	if got != "2026-01-15T12:00:00Z" {
		t.Errorf("GetBuildDate() = %q, want %q", got, "2026-01-15T12:00:00Z")
	}
}

func TestGetBuildDate_Fallback(t *testing.T) {
	// When BuildDate is empty, GetBuildDate returns the current UTC time in RFC3339 format.
	original := BuildDate
	defer func() { BuildDate = original }()

	BuildDate = ""
	got := GetBuildDate()
	if got == "" {
		t.Error("GetBuildDate() should not return empty string when BuildDate is unset")
	}
	// Ensure it parses as RFC3339.
	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Errorf("GetBuildDate() returned non-RFC3339 string %q: %v", got, err)
	}
	// Should be close to now (within a few seconds).
	diff := time.Since(parsed)
	if diff < 0 {
		diff = -diff
	}
	if diff > 5*time.Second {
		t.Errorf("GetBuildDate() fallback time %q is too far from now (%v)", got, diff)
	}
}

func TestVersionConstants(t *testing.T) {
	// Sanity-check that the default dev version looks like a semver.
	if !strings.HasPrefix(Version, "v") {
		t.Errorf("Version %q should start with 'v'", Version)
	}
	if GitCommit == "" {
		t.Error("GitCommit should not be empty (defaults to 'none')")
	}
}
