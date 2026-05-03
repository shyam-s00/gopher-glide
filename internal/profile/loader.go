package profile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load resolves and parses a profile by name using the following priority:
//
//  1. Exact file path — if name contains a path separator or ends with ".yaml"
//     it is treated as a direct file path and loaded as-is.
//  2. Local bundled copy — ./profiles/<name>.yaml relative to the working dir.
//  3. Global user config  — ~/.config/gg/profiles/<name>.yaml.
//  4. Embedded binary     — the 21 profiles baked into the binary at build time.
//
// name should be the profile slug (e.g. "flash-sale") without the .yaml suffix,
// or a full file path for custom profiles outside the standard directories.
func Load(name string) (*Profile, error) {
	// ── 1. Exact path ──────────────────────────────────────────────────────
	if isFilePath(name) {
		return loadFile(name)
	}

	slug := strings.TrimSuffix(name, ".yaml")
	filename := slug + ".yaml"

	// ── 2. Local ./profiles/ ───────────────────────────────────────────────
	localPath := filepath.Join("profiles", filename)
	if _, err := os.Stat(localPath); err == nil {
		return loadFile(localPath)
	}

	// ── 3. Global ~/.config/gg/profiles/ ──────────────────────────────────
	if home, err := os.UserHomeDir(); err == nil {
		globalPath := filepath.Join(home, ".config", "gg", "profiles", filename)
		if _, err := os.Stat(globalPath); err == nil {
			return loadFile(globalPath)
		}
	}

	// ── 4. Embedded fallback ───────────────────────────────────────────────
	return loadEmbedded(slug)
}

// ListNames returns the names of all profiles available from the embedded
// binary (i.e. the 21 shipped profiles). Names are returned without the
// .yaml suffix, sorted alphabetically.
func ListNames() []string {
	entries, err := fs.ReadDir(embeddedProfiles, "data")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	return names
}

// ── private helpers ──────────────────────────────────────────────────────────

// isFilePath returns true when name looks like a file path rather than a
// profile slug. It matches names that contain a path separator or end in .yaml.
func isFilePath(name string) bool {
	return strings.ContainsRune(name, os.PathSeparator) ||
		strings.ContainsRune(name, '/') ||
		strings.HasSuffix(name, ".yaml")
}

// loadFile reads, parses, and validates a profile from an on-disk file.
func loadFile(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read %q: %v", ErrProfileNotFound, path, err)
	}
	return parse(data, path)
}

// loadEmbedded reads, parses, and validates a profile from the embedded FS.
func loadEmbedded(slug string) (*Profile, error) {
	path := "data/" + slug + ".yaml"
	data, err := embeddedProfiles.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrProfileNotFound, slug)
	}
	return parse(data, path)
}

// parse unmarshals raw YAML bytes into a Profile and validates the result.
func parse(data []byte, src string) (*Profile, error) {
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%w: parse %q: %v", ErrInvalidProfile, src, err)
	}
	if err := validate(&p, src); err != nil {
		return nil, err
	}
	return &p, nil
}

// validate performs basic structural checks on a parsed Profile.
func validate(p *Profile, src string) error {
	if p.Name == "" {
		return fmt.Errorf("%w: %q: name is required", ErrInvalidProfile, src)
	}
	if p.DefaultPeakRPS <= 0 {
		return fmt.Errorf("%w: %q: default_peak_rps must be > 0", ErrInvalidProfile, src)
	}
	if p.DefaultDuration <= 0 {
		return fmt.Errorf("%w: %q: default_duration must be > 0", ErrInvalidProfile, src)
	}
	if len(p.Segments) == 0 {
		return fmt.Errorf("%w: %q: segments must not be empty", ErrInvalidProfile, src)
	}
	for i, s := range p.Segments {
		switch s.Type {
		case SegmentFlat, SegmentStep, SegmentLinear, SegmentExponential:
		default:
			return fmt.Errorf("%w: %q: segment[%d] unknown type %q", ErrInvalidProfile, src, i, s.Type)
		}
		if s.DurationPct < 0 || s.DurationPct > 1 {
			return fmt.Errorf("%w: %q: segment[%d] duration_pct must be in [0,1]", ErrInvalidProfile, src, i)
		}
		if s.RPSMultiplier < 0 || s.RPSMultiplier > 1 {
			return fmt.Errorf("%w: %q: segment[%d] rps_multiplier must be in [0,1]", ErrInvalidProfile, src, i)
		}
	}
	return nil
}
