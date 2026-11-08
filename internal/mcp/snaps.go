package mcp

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

const (
	maxSnapBytes     = 32 << 20
	defaultEndpoints = 20
	maxEndpoints     = 50
	maxMarksShown    = 10
	maxSchemaShown   = 5
)

// resolveSnapRef turns a snapshot reference into a file path. Only a path or a
// tag is accepted: positional ids shift whenever snapshots are added or pruned,
// so an agent could end up comparing the wrong runs.
func resolveSnapRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "":
		return "", errors.New("snapshot reference is empty; give a snapshot path (snapshot_path from stop_simulation) or a tag")
	case isDigits(ref):
		return "", fmt.Errorf("%q looks like a positional snapshot id; ids change when snapshots are added or pruned, so use the snapshot's path (snapshot_path from stop_simulation) or its tag", ref)
	case strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, snap.SnapFileExt):
		return checkSnapFile(ref)
	}
	if !snapTagPattern.MatchString(ref) {
		return "", fmt.Errorf("%q is neither a .snap path nor a valid tag (letters, digits, '.', '_' and '-')", ref)
	}
	dir, err := snap.ResolveSnapDir("")
	if err != nil {
		return "", err
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return "", err
	}
	info, err := snap.FindByTag(dir, ref)
	if err != nil {
		return "", fmt.Errorf("no snapshot tagged %q in %s (the newest snapshot with a tag is used)", ref, dir)
	}
	return info.Path, nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func checkSnapFile(path string) (string, error) {
	if !strings.HasSuffix(path, snap.SnapFileExt) {
		return "", fmt.Errorf("%s is not a snapshot file (expected a %s file)", path, snap.SnapFileExt)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("cannot read snapshot %s: %w", path, fsReason(err))
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxSnapBytes {
		return "", fmt.Errorf("snapshot %s is larger than %d MiB", path, maxSnapBytes>>20)
	}
	return path, nil
}

// loadSnap resolves and reads a snapshot, naming the file in every failure.
func loadSnap(ref string) (*snap.Snapshot, string, error) {
	path, err := resolveSnapRef(ref)
	if err != nil {
		return nil, "", err
	}
	s, err := snap.Read(path)
	if err != nil {
		return nil, path, fmt.Errorf("snapshot %s is corrupt or unreadable: %w", path, unwrapAll(err))
	}
	if s.Version == 0 {
		return nil, path, fmt.Errorf("%s is not a gg snapshot (no version field)", path)
	}
	return s, path, nil
}

func capEndpoints(n int) (int, error) {
	switch {
	case n == 0:
		return defaultEndpoints, nil
	case n < 0 || n > maxEndpoints:
		return 0, fmt.Errorf("max_endpoints must be between 1 and %d", maxEndpoints)
	}
	return n, nil
}

func r2(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return math.Round(f*100) / 100
}

func r4(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return math.Round(f*10000) / 10000
}
