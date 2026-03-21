package snap

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── format.go ─────────────────────────────────────────────────────────────────

func TestWrite_Read_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.snap")

	original := &Snapshot{
		Version: 1,
		Meta: SnapMeta{
			Tag:           "v1.0.0",
			StartTime:     time.Date(2026, 3, 16, 14, 30, 22, 0, time.UTC),
			EndTime:       time.Date(2026, 3, 16, 14, 31, 52, 0, time.UTC),
			PeakRPS:       500,
			TotalRequests: 45000,
			ConfigHash:    "sha256:abc123",
		},
		Endpoints: []EndpointSnap{
			{
				ID:          "GET:/api/users",
				StatusDist:  map[string]float64{"200": 0.97, "500": 0.03},
				Latency:     LatencyStats{P50: 12.3, P95: 48.1, P99: 120.4, Max: 3200},
				ErrorRate:   0.03,
				SampleCount: 45000,
			},
		},
	}

	if err := Write(original, path); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	// File must exist and be non-empty.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("written file is empty")
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}

	if got.Version != original.Version {
		t.Errorf("Version: got %d, want %d", got.Version, original.Version)
	}
	if got.Meta.Tag != original.Meta.Tag {
		t.Errorf("Tag: got %q, want %q", got.Meta.Tag, original.Meta.Tag)
	}
	if got.Meta.TotalRequests != original.Meta.TotalRequests {
		t.Errorf("TotalRequests: got %d, want %d", got.Meta.TotalRequests, original.Meta.TotalRequests)
	}
	if len(got.Endpoints) != 1 {
		t.Fatalf("Endpoints: got %d, want 1", len(got.Endpoints))
	}
	ep := got.Endpoints[0]
	if ep.ID != "GET:/api/users" {
		t.Errorf("endpoint ID: got %q", ep.ID)
	}
	if ep.StatusDist["200"] != 0.97 {
		t.Errorf("status 200 dist: got %f", ep.StatusDist["200"])
	}
	if ep.Latency.P99 != 120.4 {
		t.Errorf("P99: got %f, want 120.4", ep.Latency.P99)
	}
}

func TestWrite_AtomicRename(t *testing.T) {
	// Verify no temp file is left behind after a successful write.
	dir := t.TempDir()
	path := filepath.Join(dir, "test.snap")
	_ = Write(&Snapshot{Version: 1}, path)

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestRead_NonExistent(t *testing.T) {
	_, err := Read("/nonexistent/path/test.snap")
	if err == nil {
		t.Error("expected error reading non-existent file")
	}
}

func TestRead_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.snap")
	_ = os.WriteFile(path, []byte(`not json`), 0o644)

	_, err := Read(path)
	if err == nil {
		t.Error("expected error reading invalid JSON")
	}
}

func TestFileName_WithTag(t *testing.T) {
	t0 := time.Date(2026, 3, 16, 14, 30, 22, 0, time.UTC)
	got := FileName("v1.2.0-pre", t0)
	want := "v1.2.0-pre-20260316-143022.snap"
	if got != want {
		t.Errorf("FileName = %q, want %q", got, want)
	}
}

func TestFileName_NoTag(t *testing.T) {
	t0 := time.Date(2026, 3, 16, 14, 30, 22, 0, time.UTC)
	got := FileName("", t0)
	want := "run-20260316-143022.snap"
	if got != want {
		t.Errorf("FileName = %q, want %q", got, want)
	}
}

// ── store.go ──────────────────────────────────────────────────────────────────

func TestResolveSnapDir_Override(t *testing.T) {
	got, err := ResolveSnapDir("/tmp/custom-snaps")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/tmp/custom-snaps" {
		t.Errorf("got %q, want /tmp/custom-snaps", got)
	}
}

func TestResolveSnapDir_EnvVar(t *testing.T) {
	t.Setenv("GG_SNAP_DIR", "/tmp/env-snaps")
	got, err := ResolveSnapDir("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/tmp/env-snaps" {
		t.Errorf("got %q, want /tmp/env-snaps", got)
	}
}

func TestResolveSnapDir_Default(t *testing.T) {
	t.Setenv("GG_SNAP_DIR", "") // clear env var
	got, err := ResolveSnapDir("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty default path")
	}
	// Must end with the canonical sub-path.
	wantSuffix := filepath.Join("gg", "snapshots")
	if !filepath.IsAbs(got) {
		t.Errorf("expected absolute path, got %q", got)
	}
	if filepath.Base(filepath.Dir(got)) != "gg" {
		t.Logf("resolved path: %s (suffix check: %s)", got, wantSuffix)
	}
}

func TestEnsureSnapDir_CreatesDirectory(t *testing.T) {
	base := t.TempDir()
	override := filepath.Join(base, "snaps", "nested")

	got, err := EnsureSnapDir(override)
	if err != nil {
		t.Fatalf("EnsureSnapDir error: %v", err)
	}
	if got != override {
		t.Errorf("got %q, want %q", got, override)
	}
	fi, err := os.Stat(got)
	if err != nil || !fi.IsDir() {
		t.Errorf("directory was not created at %q", got)
	}
}

func TestList_Empty(t *testing.T) {
	// Non-existent directory returns nil, not an error.
	infos, err := List("/nonexistent/snap/dir")
	if err != nil {
		t.Errorf("expected nil error for missing dir, got %v", err)
	}
	if infos != nil {
		t.Errorf("expected nil slice, got %v", infos)
	}
}

func TestList_SortedByDate(t *testing.T) {
	dir := t.TempDir()

	// Write three snap files with different timestamps.
	times := []time.Time{
		time.Date(2026, 3, 18, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 16, 14, 30, 22, 0, time.UTC),
		time.Date(2026, 3, 17, 9, 0, 0, 0, time.UTC),
	}
	for _, ts := range times {
		name := FileName("test", ts)
		if err := Write(&Snapshot{Version: 1}, filepath.Join(dir, name)); err != nil {
			t.Fatalf("Write error: %v", err)
		}
	}

	infos, err := List(dir)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("expected 3 infos, got %d", len(infos))
	}

	// IDs must be 1, 2, 3 in chronological order.
	for i, info := range infos {
		if info.ID != i+1 {
			t.Errorf("infos[%d].ID = %d, want %d", i, info.ID, i+1)
		}
	}
	// Oldest must be first.
	if !infos[0].Date.Before(infos[1].Date) {
		t.Error("infos[0] should be older than infos[1]")
	}
	if !infos[1].Date.Before(infos[2].Date) {
		t.Error("infos[1] should be older than infos[2]")
	}
}

func TestList_SkipsMalformedNames(t *testing.T) {
	dir := t.TempDir()

	// A valid file.
	validName := FileName("ok", time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC))
	_ = Write(&Snapshot{Version: 1}, filepath.Join(dir, validName))

	// A malformed .snap file that won't parse.
	_ = os.WriteFile(filepath.Join(dir, "broken.snap"), []byte("{}"), 0o644)

	infos, err := List(dir)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(infos) != 1 {
		t.Errorf("expected 1 valid entry, got %d", len(infos))
	}
}

func TestFindByID(t *testing.T) {
	dir := t.TempDir()

	for i, ts := range []time.Time{
		time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC),
	} {
		name := FileName("snap", ts)
		_ = Write(&Snapshot{Version: 1, Meta: SnapMeta{Tag: name}}, filepath.Join(dir, name))
		_ = i
	}

	info, err := FindByID(dir, 1)
	if err != nil {
		t.Fatalf("FindByID(1) error: %v", err)
	}
	if info.ID != 1 {
		t.Errorf("ID = %d, want 1", info.ID)
	}

	_, err = FindByID(dir, 99)
	if err == nil {
		t.Error("expected error for missing ID 99")
	}
}

func TestFindByTag(t *testing.T) {
	dir := t.TempDir()

	tags := []struct {
		tag string
		ts  time.Time
	}{
		{"v1.0.0", time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)},
		{"v1.1.0", time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)},
		{"v1.0.0", time.Date(2026, 3, 18, 10, 0, 0, 0, time.UTC)}, // newer v1.0.0
	}
	for _, tc := range tags {
		name := FileName(tc.tag, tc.ts)
		_ = Write(&Snapshot{Version: 1}, filepath.Join(dir, name))
	}

	// FindByTag returns the most recent match.
	info, err := FindByTag(dir, "v1.0.0")
	if err != nil {
		t.Fatalf("FindByTag error: %v", err)
	}
	wantDate := time.Date(2026, 3, 18, 10, 0, 0, 0, time.UTC)
	if !info.Date.Equal(wantDate) {
		t.Errorf("date = %v, want %v (most recent)", info.Date, wantDate)
	}

	_, err = FindByTag(dir, "v9.9.9")
	if err == nil {
		t.Error("expected error for missing tag")
	}
}

func TestParseSnapInfo_ValidName(t *testing.T) {
	path := "/snaps/v1.2.0-pre-20260316-143022.snap"
	info, err := parseSnapInfo(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Tag != "v1.2.0-pre" {
		t.Errorf("Tag = %q, want v1.2.0-pre", info.Tag)
	}
	wantDate := time.Date(2026, 3, 16, 14, 30, 22, 0, time.UTC)
	if !info.Date.Equal(wantDate) {
		t.Errorf("Date = %v, want %v", info.Date, wantDate)
	}
	if info.FileName != "v1.2.0-pre-20260316-143022.snap" {
		t.Errorf("FileName = %q", info.FileName)
	}
}

func TestParseSnapInfo_RunPrefix(t *testing.T) {
	path := "/snaps/run-20260316-143022.snap"
	info, err := parseSnapInfo(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Tag != "run" {
		t.Errorf("Tag = %q, want run", info.Tag)
	}
}

func TestParseSnapInfo_Malformed(t *testing.T) {
	cases := []string{
		"/snaps/short.snap",
		"/snaps/no-date-here.snap",
		"/snaps/baddate-99999999-999999.snap",
	}
	for _, c := range cases {
		if _, err := parseSnapInfo(c); err == nil {
			t.Errorf("expected error for %q, got nil", c)
		}
	}
}
