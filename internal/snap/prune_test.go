package snap

import (
	"fmt"
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// days returns a UTC time that is n days before a fixed reference point.
// Using a fixed reference makes test assertions deterministic.
var refNow = time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)

func daysAgo(n int) time.Time {
	return refNow.Add(-time.Duration(n) * 24 * time.Hour)
}

// makeTestInfos builds a slice of SnapInfo with predictable IDs, tags, dates
// and paths so each test starts from a well-known state.
func makeTestInfos() []SnapInfo {
	return []SnapInfo{
		{ID: 1, Tag: "v1.0", Date: daysAgo(30), Path: "/snaps/v1.0-30d.snap", FileName: "v1.0-30d.snap"},
		{ID: 2, Tag: "v1.1", Date: daysAgo(20), Path: "/snaps/v1.1-20d.snap", FileName: "v1.1-20d.snap"},
		{ID: 3, Tag: "run", Date: daysAgo(10), Path: "/snaps/run-10d.snap", FileName: "run-10d.snap"},
		{ID: 4, Tag: "v2.0", Date: daysAgo(5), Path: "/snaps/v2.0-5d.snap", FileName: "v2.0-5d.snap"},
		{ID: 5, Tag: "v2.0", Date: daysAgo(1), Path: "/snaps/v2.0-1d.snap", FileName: "v2.0-1d.snap"},
	}
}

func candidatePaths(cs []PruneCandidate) []string {
	ps := make([]string, len(cs))
	for i, c := range cs {
		ps[i] = c.Path
	}
	return ps
}

// ── SelectForPrune tests ──────────────────────────────────────────────────────

func TestSelectForPrune_Empty(t *testing.T) {
	result := SelectForPrune(nil, PruneOptions{KeepLast: 3})
	if len(result) != 0 {
		t.Fatalf("expected 0 candidates for empty input, got %d", len(result))
	}
}

func TestSelectForPrune_KeepLast(t *testing.T) {
	infos := makeTestInfos()
	result := SelectForPrune(infos, PruneOptions{KeepLast: 2, Now: refNow})

	// Keep the 2 newest (IDs 4, 5); delete the 3 oldest (IDs 1, 2, 3).
	if len(result) != 3 {
		t.Fatalf("keep-last 2: expected 3 candidates, got %d", len(result))
	}
	for _, c := range result {
		if c.ID == 4 || c.ID == 5 {
			t.Errorf("keep-last 2: ID %d should have been kept, but was selected for deletion", c.ID)
		}
	}
}

func TestSelectForPrune_KeepLast_LargerThanTotal(t *testing.T) {
	infos := makeTestInfos()
	result := SelectForPrune(infos, PruneOptions{KeepLast: 10, Now: refNow})
	if len(result) != 0 {
		t.Fatalf("keep-last > total: expected 0 candidates, got %d", len(result))
	}
}

func TestSelectForPrune_KeepLast_ExactCount(t *testing.T) {
	infos := makeTestInfos()
	result := SelectForPrune(infos, PruneOptions{KeepLast: len(infos), Now: refNow})
	if len(result) != 0 {
		t.Fatalf("keep-last == total: expected 0 candidates, got %d", len(result))
	}
}

func TestSelectForPrune_OlderThan(t *testing.T) {
	infos := makeTestInfos()
	// 15 days → snaps at 30d and 20d are candidates; 10d, 5d, 1d are kept.
	result := SelectForPrune(infos, PruneOptions{OlderThan: 15 * 24 * time.Hour, Now: refNow})

	if len(result) != 2 {
		t.Fatalf("older-than 15d: expected 2 candidates, got %d: %v", len(result), candidatePaths(result))
	}
	for _, c := range result {
		if c.ID != 1 && c.ID != 2 {
			t.Errorf("older-than 15d: unexpected candidate ID %d (%s)", c.ID, c.FileName)
		}
	}
}

func TestSelectForPrune_Tag(t *testing.T) {
	infos := makeTestInfos()
	result := SelectForPrune(infos, PruneOptions{Tag: "v2.0", Now: refNow})

	if len(result) != 2 {
		t.Fatalf("tag v2.0: expected 2 candidates, got %d", len(result))
	}
	for _, c := range result {
		if c.Tag != "v2.0" {
			t.Errorf("tag v2.0: unexpected tag %q on candidate %s", c.Tag, c.FileName)
		}
	}
}

func TestSelectForPrune_Tag_NoMatch(t *testing.T) {
	infos := makeTestInfos()
	result := SelectForPrune(infos, PruneOptions{Tag: "nonexistent", Now: refNow})
	if len(result) != 0 {
		t.Fatalf("tag nonexistent: expected 0 candidates, got %d", len(result))
	}
}

func TestSelectForPrune_IDs(t *testing.T) {
	infos := makeTestInfos()
	result := SelectForPrune(infos, PruneOptions{IDs: []int{1, 3, 5}, Now: refNow})

	if len(result) != 3 {
		t.Fatalf("ids 1,3,5: expected 3 candidates, got %d", len(result))
	}
	gotIDs := map[int]bool{}
	for _, c := range result {
		gotIDs[c.ID] = true
	}
	for _, want := range []int{1, 3, 5} {
		if !gotIDs[want] {
			t.Errorf("ids 1,3,5: expected ID %d in candidates", want)
		}
	}
}

func TestSelectForPrune_IDs_InvalidID(t *testing.T) {
	infos := makeTestInfos()
	// ID 99 doesn't exist — should silently produce no candidate for it.
	result := SelectForPrune(infos, PruneOptions{IDs: []int{99}, Now: refNow})
	if len(result) != 0 {
		t.Fatalf("ids 99 (nonexistent): expected 0 candidates, got %d", len(result))
	}
}

func TestSelectForPrune_Combined_OrSemantics(t *testing.T) {
	infos := makeTestInfos()
	// --tag v1.0 → ID 1
	// --ids 3    → ID 3
	// Both should appear; ID 2, 4, 5 should not.
	result := SelectForPrune(infos, PruneOptions{Tag: "v1.0", IDs: []int{3}, Now: refNow})

	if len(result) != 2 {
		t.Fatalf("combined tag+ids: expected 2 candidates, got %d: %v", len(result), candidatePaths(result))
	}
	gotIDs := map[int]bool{}
	for _, c := range result {
		gotIDs[c.ID] = true
	}
	if !gotIDs[1] || !gotIDs[3] {
		t.Errorf("combined tag+ids: expected IDs 1 and 3, got %v", gotIDs)
	}
}

func TestSelectForPrune_Combined_NoDuplicates(t *testing.T) {
	infos := makeTestInfos()
	// --ids 1 AND --older-than 15d both match ID 1.
	// It should appear exactly once.
	result := SelectForPrune(infos, PruneOptions{
		IDs:       []int{1},
		OlderThan: 15 * 24 * time.Hour,
		Now:       refNow,
	})

	seen := map[string]int{}
	for _, c := range result {
		seen[c.Path]++
	}
	for path, count := range seen {
		if count > 1 {
			t.Errorf("duplicate candidate: %s appears %d times", path, count)
		}
	}
}

func TestSelectForPrune_CandidatesSortedOldestFirst(t *testing.T) {
	infos := makeTestInfos()
	result := SelectForPrune(infos, PruneOptions{KeepLast: 1, Now: refNow})

	for i := 1; i < len(result); i++ {
		if result[i].Date.Before(result[i-1].Date) {
			t.Errorf("candidates not sorted oldest-first at index %d", i)
		}
	}
}

// ── ParseOlderThan tests ──────────────────────────────────────────────────────

func TestParseOlderThan(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"720h", 720 * time.Hour, false},
		{"48h30m", 48*time.Hour + 30*time.Minute, false},
		{"0d", 0, true},  // 0 is not positive
		{"-1d", 0, true}, // negative is invalid
		{"abc", 0, true},
		{"30", 0, true}, // missing unit
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("input=%q", tt.input), func(t *testing.T) {
			got, err := ParseOlderThan(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseOlderThan(%q): err=%v, wantErr=%v", tt.input, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("ParseOlderThan(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ── ParseIDs tests ─────────────────────────────────────────────────────────────

func TestParseIDs(t *testing.T) {
	tests := []struct {
		input   string
		want    []int
		wantErr bool
	}{
		{"", nil, false},
		{"1", []int{1}, false},
		{"1,3,5", []int{1, 3, 5}, false},
		{"1, 3, 5", []int{1, 3, 5}, false}, // spaces tolerated
		{"0", nil, true},                   // 0 is not positive
		{"-1", nil, true},
		{"abc", nil, true},
		{"1,abc,3", nil, true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("input=%q", tt.input), func(t *testing.T) {
			got, err := ParseIDs(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseIDs(%q): err=%v, wantErr=%v", tt.input, err, tt.wantErr)
			}
			if err == nil {
				if len(got) != len(tt.want) {
					t.Fatalf("ParseIDs(%q) = %v, want %v", tt.input, got, tt.want)
				}
				for i := range got {
					if got[i] != tt.want[i] {
						t.Errorf("ParseIDs(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.want[i])
					}
				}
			}
		})
	}
}

// ── BuildPruneReport tests ────────────────────────────────────────────────────

func TestBuildPruneReport_DryRun(t *testing.T) {
	candidates := []PruneCandidate{
		{SnapInfo: SnapInfo{ID: 1, Tag: "v1.0", Date: daysAgo(30), Path: "/s/a.snap", FileName: "a.snap"}, Reason: "id 1 selected"},
	}
	report := BuildPruneReport("/snaps", candidates, true, 0, nil)

	if !report.DryRun {
		t.Error("expected DryRun=true")
	}
	if report.Deleted != 0 {
		t.Errorf("dry run: Deleted should be 0, got %d", report.Deleted)
	}
	if len(report.Candidates) != 1 {
		t.Errorf("expected 1 candidate in report, got %d", len(report.Candidates))
	}
	if report.Candidates[0].ID != 1 {
		t.Errorf("candidate ID: want 1, got %d", report.Candidates[0].ID)
	}
}

func TestBuildPruneReport_JSONRoundtrip(t *testing.T) {
	candidates := []PruneCandidate{
		{SnapInfo: SnapInfo{ID: 2, Tag: "v2.0", Date: daysAgo(5), Path: "/s/b.snap", FileName: "b.snap"}, Reason: "tag \"v2.0\" matches"},
	}
	report := BuildPruneReport("/snaps", candidates, false, 1, nil)
	js, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON(): %v", err)
	}
	if js == "" {
		t.Fatal("JSON() returned empty string")
	}
}
