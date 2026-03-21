package snap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ── Directory resolution ──────────────────────────────────────────────────────

// ResolveSnapDir returns the directory where snapshots are stored.
//
// Priority (highest → lowest):
//  1. override argument (non-empty string)
//  2. GG_SNAP_DIR environment variable
//  3. {os.UserConfigDir()}/gg/snapshots
func ResolveSnapDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if env := os.Getenv("GG_SNAP_DIR"); env != "" {
		return env, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("snap: resolve user config dir: %w", err)
	}
	return filepath.Join(base, "gg", "snapshots"), nil
}

// EnsureSnapDir resolves the snap directory, converts it to an absolute path,
// and creates it (plus any missing parents) if it does not already exist.
// Returns the resolved absolute path.
func EnsureSnapDir(override string) (string, error) {
	dir, err := ResolveSnapDir(override)
	if err != nil {
		return "", err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("snap: resolve absolute path %q: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("snap: create directory %q: %w", dir, err)
	}
	return dir, nil
}

// ── Snapshot listing ──────────────────────────────────────────────────────────

// SnapInfo summarises a snapshot file without deserialising the full JSON.
type SnapInfo struct {
	ID       int       // 1-based, stable as long as the directory contents don't change
	Tag      string    // prefix extracted from the file name
	Date     time.Time // UTC timestamp extracted from the file name
	Path     string    // absolute file path
	FileName string    // base file name only
}

// List returns all valid SnapInfo entries found in dir, sorted oldest-first so
// IDs are stable. Files that do not match the expected naming pattern are
// silently skipped. Returns nil (not an error) when the directory does not
// exist yet.
func List(dir string) ([]SnapInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("snap: list %q: %w", dir, err)
	}

	var infos []SnapInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), SnapFileExt) {
			continue
		}
		info, err := parseSnapInfo(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // skip malformed file names silently
		}
		infos = append(infos, info)
	}

	// Sort by date so IDs are deterministic across runs.
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Date.Before(infos[j].Date)
	})
	for i := range infos {
		infos[i].ID = i + 1
	}
	return infos, nil
}

// FindByID returns the SnapInfo whose 1-based ID matches id.
// Returns an error when no match is found.
func FindByID(dir string, id int) (*SnapInfo, error) {
	infos, err := List(dir)
	if err != nil {
		return nil, err
	}
	for i := range infos {
		if infos[i].ID == id {
			return &infos[i], nil
		}
	}
	return nil, fmt.Errorf("snap: no snapshot with ID %d in %q", id, dir)
}

// FindByTag returns the most-recent SnapInfo whose Tag matches tag.
// Returns an error when no match is found.
func FindByTag(dir, tag string) (*SnapInfo, error) {
	infos, err := List(dir)
	if err != nil {
		return nil, err
	}
	// Iterate in reverse (newest first) so we return the most-recent match.
	for i := len(infos) - 1; i >= 0; i-- {
		if infos[i].Tag == tag {
			return &infos[i], nil
		}
	}
	return nil, fmt.Errorf("snap: no snapshot with tag %q in %q", tag, dir)
}

// ── File name parsing ─────────────────────────────────────────────────────────

// parseSnapInfo derives a SnapInfo from an absolute .snap file path.
//
// Expected name format: {tag}-{YYYYMMDD-HHMMSS}.snap
// The date suffix is always exactly 15 characters ("20060102-150405").
func parseSnapInfo(path string) (SnapInfo, error) {
	base := filepath.Base(path)
	name := strings.TrimSuffix(base, SnapFileExt)

	// Date suffix: "20060102-150405" = 15 chars + 1 separator dash
	const dateSuffixLen = 15
	if len(name) < dateSuffixLen+1 {
		return SnapInfo{}, fmt.Errorf("snap: file name too short to parse: %q", base)
	}

	dateStr := name[len(name)-dateSuffixLen:]
	tag := strings.TrimSuffix(name[:len(name)-dateSuffixLen], "-")

	t, err := time.ParseInLocation("20060102-150405", dateStr, time.UTC)
	if err != nil {
		return SnapInfo{}, fmt.Errorf("snap: bad date in file name %q: %w", base, err)
	}

	return SnapInfo{
		Tag:      tag,
		Date:     t,
		Path:     path,
		FileName: base,
	}, nil
}
