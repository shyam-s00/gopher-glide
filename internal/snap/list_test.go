package snap

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListAll_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	summaries, err := ListAll(dir)
	if err != nil {
		t.Fatalf("ListAll on empty dir: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("expected 0 summaries, got %d", len(summaries))
	}
}

func TestListAll_NonExistentDir(t *testing.T) {
	// List() returns nil, nil for a non-existent directory.
	// ListAll wraps List and returns an empty (or nil) slice with no error.
	summaries, err := ListAll("/nonexistent/snap/dir")
	if err != nil {
		t.Fatalf("ListAll on non-existent dir should return nil error, got %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("expected empty summaries for non-existent dir, got %v", summaries)
	}
}

func TestListAll_PopulatesFields(t *testing.T) {
	dir := t.TempDir()

	snap1 := &Snapshot{
		Version: 1,
		Meta: SnapMeta{
			Tag:           "v1",
			TotalRequests: 1000,
			PeakRPS:       50,
		},
		Endpoints: []EndpointSnap{
			{ID: "GET:/api/a"},
			{ID: "POST:/api/b"},
		},
	}
	ts1 := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
	name1 := FileName("v1", ts1)
	if err := Write(snap1, filepath.Join(dir, name1)); err != nil {
		t.Fatalf("Write snap1: %v", err)
	}

	snap2 := &Snapshot{
		Version: 1,
		Meta: SnapMeta{
			Tag:           "v2",
			TotalRequests: 2000,
			PeakRPS:       100,
		},
		Endpoints: []EndpointSnap{
			{ID: "GET:/api/c"},
		},
	}
	ts2 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)
	name2 := FileName("v2", ts2)
	if err := Write(snap2, filepath.Join(dir, name2)); err != nil {
		t.Fatalf("Write snap2: %v", err)
	}

	summaries, err := ListAll(dir)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}

	// Summaries are sorted oldest-first (same as List).
	s1 := summaries[0]
	if s1.TotalRequests != 1000 {
		t.Errorf("summaries[0].TotalRequests: want 1000, got %d", s1.TotalRequests)
	}
	if s1.PeakRPS != 50 {
		t.Errorf("summaries[0].PeakRPS: want 50, got %d", s1.PeakRPS)
	}
	if s1.EndpointCount != 2 {
		t.Errorf("summaries[0].EndpointCount: want 2, got %d", s1.EndpointCount)
	}

	s2 := summaries[1]
	if s2.TotalRequests != 2000 {
		t.Errorf("summaries[1].TotalRequests: want 2000, got %d", s2.TotalRequests)
	}
	if s2.EndpointCount != 1 {
		t.Errorf("summaries[1].EndpointCount: want 1, got %d", s2.EndpointCount)
	}
}

func TestListAll_CorruptFileIncludedWithZeroFields(t *testing.T) {
	dir := t.TempDir()

	// Write one valid snap.
	ts := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
	name := FileName("ok", ts)
	if err := Write(&Snapshot{Version: 1, Meta: SnapMeta{TotalRequests: 42, PeakRPS: 5}}, filepath.Join(dir, name)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Write a corrupt snap (valid file name, invalid JSON).
	ts2 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)
	name2 := FileName("corrupt", ts2)
	if err := os.WriteFile(filepath.Join(dir, name2), []byte(`not valid json`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	summaries, err := ListAll(dir)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries (one good, one corrupt), got %d", len(summaries))
	}

	// Valid snap should have populated fields.
	if summaries[0].TotalRequests != 42 {
		t.Errorf("good snap TotalRequests: want 42, got %d", summaries[0].TotalRequests)
	}
	// Corrupt snap should have zero fields but still be present.
	if summaries[1].TotalRequests != 0 {
		t.Errorf("corrupt snap TotalRequests: want 0, got %d", summaries[1].TotalRequests)
	}
}

func TestListAll_SkipsDirs(t *testing.T) {
	dir := t.TempDir()

	// Create a sub-directory that ends in .snap — it should be ignored.
	_ = os.Mkdir(filepath.Join(dir, "subdir.snap"), 0o755)

	ts := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
	name := FileName("real", ts)
	if err := Write(&Snapshot{Version: 1}, filepath.Join(dir, name)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	summaries, err := ListAll(dir)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	// Only the real file should be in the results.
	if len(summaries) != 1 {
		t.Errorf("expected 1 summary, got %d", len(summaries))
	}
}
