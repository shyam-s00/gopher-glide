package snap

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// SnapFileExt is the file extension used for all snapshot files.
const SnapFileExt = ".snap"

// Write serialises snap as indented JSON and atomically writes it to path.
// The file is written to a sibling temp file first, then renamed so a partial
// write never leaves a corrupt .snap on disk.
// The parent directory must already exist; use EnsureSnapDir to create it.
func Write(snap *Snapshot, path string) (err error) {
	// Write to a temp file in the same directory, so the final rename is atomic
	// on all platforms (same filesystem).
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("snap: create temp file %q: %w", tmp, err)
	}

	// Ensure cleanup if we exit early
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err = enc.Encode(snap); err != nil {
		return fmt.Errorf("snap: encode snapshot: %w", err)
	}

	if err = f.Close(); err != nil {
		return fmt.Errorf("snap: close temp file: %w", err)
	}

	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("snap: rename %q → %q: %w", tmp, path, err)
	}
	return nil
}

// Read deserialises a Snapshot from the JSON file at path.
func Read(path string) (*Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("snap: open %q: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()

	var s Snapshot
	if err := json.NewDecoder(f).Decode(&s); err != nil {
		return nil, fmt.Errorf("snap: decode %q: %w", path, err)
	}
	return &s, nil
}

// FileName returns the canonical file name for a snapshot.
//
//	tag="v1.2.0-pre"  → "v1.2.0-pre-20260316-143022.snap"
//	tag=""            → "run-20260316-143022.snap"
func FileName(tag string, t time.Time) string {
	if tag == "" {
		tag = "run"
	}
	return fmt.Sprintf("%s-%s%s", tag, t.UTC().Format("20060102-150405"), SnapFileExt)
}
