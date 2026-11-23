package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

func listSnaps(t *testing.T) (dir string) {
	t.Helper()
	dir = snapDirEnv(t)
	at := func(day int) time.Time { return time.Date(2026, 11, day, 9, 0, 0, 0, time.UTC) }
	writeSnap(t, dir, "release-1", at(1), snapMeta("release-1", nil), ep("GET:/a", 1000, 100, 0, nil))
	writeSnap(t, dir, "release-2", at(2), snapMeta("release-2", func(m *snap.SnapMeta) {
		m.Marks = []snap.Mark{{Label: "x", ElapsedS: 3, Source: "agent"}}
		m.TotalRequests = 2000
	}), ep("GET:/a", 1500, 120, 0.02, nil), ep("GET:/b", 500, 300, 0.10, nil))
	writeSnap(t, dir, "baseline", at(3), snapMeta("baseline", func(m *snap.SnapMeta) {
		m.BiasEvents = []snap.BiasEvent{{Amount: 5, Cumulative: 5, ElapsedS: 2}}
	}), ep("GET:/a", 3000, 50, 0, nil))
	return dir
}

func TestSnapList_NewestFirstWithHeadlineNumbers(t *testing.T) {
	dir := listSnaps(t)
	out, err := snapList(ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Dir != dir || out.Total != 3 || out.Omitted != 0 || len(out.Snapshots) != 3 {
		t.Fatalf("out = %+v", out)
	}
	var tags []string
	for _, e := range out.Snapshots {
		tags = append(tags, e.Tag)
	}
	if strings.Join(tags, ",") != "baseline,release-2,release-1" {
		t.Errorf("order = %v", tags)
	}

	r2 := out.Snapshots[1]
	if r2.TotalRequests != 2000 || r2.Endpoints != 2 || r2.DurationS != 60 || !r2.HasMarks || r2.HasBias {
		t.Errorf("release-2 = %+v", r2)
	}
	// (1500*0.02 + 500*0.10) / 2000 = 0.04; percentiles are the worst endpoint's.
	if r2.ErrorRate != 0.04 || r2.P99Ms != 300 || r2.P95Ms != 150 {
		t.Errorf("release-2 numbers = %+v", r2)
	}
	if b := out.Snapshots[0]; !b.HasBias || b.HasMarks || b.Path != filepath.Join(dir, snap.FileName("baseline", time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC))) {
		t.Errorf("baseline = %+v", b)
	}
	if _, err := time.Parse(time.RFC3339, r2.CreatedAt); err != nil {
		t.Errorf("created_at = %q", r2.CreatedAt)
	}
}

func TestSnapList_EntriesAreUsableAsReferences(t *testing.T) {
	listSnaps(t)
	out, _ := snapList(ListInput{})
	for _, e := range out.Snapshots {
		if _, _, err := loadSnap(e.Path); err != nil {
			t.Errorf("path %s not usable: %v", e.Path, err)
		}
	}
	if _, _, err := loadSnap(out.Snapshots[0].Tag); err != nil {
		t.Errorf("tag not usable: %v", err)
	}
}

func TestSnapList_TagGlobAndLimit(t *testing.T) {
	listSnaps(t)
	cases := []struct {
		name  string
		in    ListInput
		tags  string
		total int
		omit  int
	}{
		{"glob", ListInput{TagGlob: "release-*"}, "release-2,release-1", 2, 0},
		{"exact", ListInput{TagGlob: "baseline"}, "baseline", 1, 0},
		{"no match", ListInput{TagGlob: "nightly-*"}, "", 0, 0},
		{"limit leaves a count", ListInput{Limit: 2}, "baseline,release-2", 3, 1},
		{"glob and limit", ListInput{TagGlob: "release-*", Limit: 1}, "release-2", 2, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := snapList(c.in)
			if err != nil {
				t.Fatal(err)
			}
			var tags []string
			for _, e := range out.Snapshots {
				tags = append(tags, e.Tag)
			}
			if strings.Join(tags, ",") != c.tags || out.Total != c.total || out.Omitted != c.omit {
				t.Errorf("tags %v total %d omitted %d", tags, out.Total, out.Omitted)
			}
			if out.Snapshots == nil {
				t.Error("snapshots must be an empty list, not null")
			}
		})
	}
}

func TestSnapList_Validation(t *testing.T) {
	listSnaps(t)
	for _, in := range []ListInput{{Limit: -1}, {Limit: 101}, {TagGlob: "[bad"}} {
		if _, err := snapList(in); err == nil {
			t.Errorf("%+v should be refused", in)
		}
	}
}

func TestSnapList_EmptyAndMissingDirectory(t *testing.T) {
	snapDirEnv(t)
	out, err := snapList(ListInput{})
	if err != nil || out.Total != 0 || out.Snapshots == nil || len(out.Snapshots) != 0 {
		t.Errorf("empty dir = %+v %v", out, err)
	}
	t.Setenv("GG_SNAP_DIR", filepath.Join(t.TempDir(), "never-created"))
	if out, err = snapList(ListInput{}); err != nil || out.Total != 0 {
		t.Errorf("missing dir = %+v %v", out, err)
	}
}

func TestSnapList_CorruptFileIsListedWithTheReason(t *testing.T) {
	dir := listSnaps(t)
	bad := filepath.Join(dir, snap.FileName("broken", time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)))
	if err := os.WriteFile(bad, []byte("not a snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := snapList(ListInput{})
	if err != nil || out.Total != 4 {
		t.Fatalf("out = %+v %v", out, err)
	}
	first := out.Snapshots[0]
	if first.Tag != "broken" || first.Error == "" || first.TotalRequests != 0 {
		t.Errorf("broken entry = %+v", first)
	}
	if out.Snapshots[1].Error != "" {
		t.Errorf("good entries must be unaffected: %+v", out.Snapshots[1])
	}
}

func TestSnapList_ThroughTheProtocol(t *testing.T) {
	listSnaps(t)
	cs, _ := simClient(t, "normal", nil)
	var out ListOutput
	res := call(t, cs, "snap_list", map[string]any{"tag_glob": "release-*", "limit": 1}, &out)
	if res.IsError || len(out.Snapshots) != 1 || out.Snapshots[0].Tag != "release-2" || out.Omitted != 1 {
		t.Fatalf("snap_list = %+v %s", out, errText(res))
	}
	if res := call(t, cs, "snap_list", map[string]any{"limit": 500}, nil); !res.IsError {
		t.Error("an out-of-range limit must be an error")
	}
}
