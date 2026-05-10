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
//  2. Global user config  — ~/.config/gg/profiles/<name>.yaml.
//     Built-in names are reserved: if the file exists but the slug matches a
//     built-in profile, Load returns ErrBuiltInProfileConflict instead of
//     silently overriding the canonical definition.
//  3. Embedded binary     — the 21 profiles baked into the binary at build time.
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

	// ── 2. Global ~/.config/gg/profiles/ ──────────────────────────────────
	if home, err := os.UserHomeDir(); err == nil {
		globalPath := filepath.Join(home, ".config", "gg", "profiles", filename)
		if _, err := os.Stat(globalPath); err == nil {
			// Collision guard: built-in names are reserved and cannot be
			// overridden by a file in the user config directory.
			if IsBuiltIn(slug) {
				return nil, fmt.Errorf(
					"%w: %q is a built-in profile and cannot be overridden.\n"+
						"Rename the file in ~/.config/gg/profiles/ to a custom name and try again",
					ErrBuiltInProfileConflict, slug,
				)
			}
			return loadFile(globalPath)
		}
	}

	// ── 3. Embedded fallback ───────────────────────────────────────────────
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

// IsBuiltIn reports whether name matches one of the 21 shipped built-in
// profiles. The .yaml extension is stripped before comparison.
func IsBuiltIn(name string) bool {
	slug := strings.TrimSuffix(name, ".yaml")
	for _, n := range ListNames() {
		if n == slug {
			return true
		}
	}
	return false
}

// ListCustomNames returns the names of all profile YAML files found in
// ~/.config/gg/profiles/ (without the .yaml extension). Names are returned
// in directory order. Names that collide with built-in profiles are included;
// callers can use IsBuiltIn to detect and surface conflicts.
func ListCustomNames() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, ".config", "gg", "profiles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	return names
}

// ExportEmbedded copies the embedded YAML for the named built-in profile to
// ~/.config/gg/profiles/<name>.yaml and returns the destination path.
// The directory is created if it does not already exist.
//
// Because built-in names are reserved, the exported file cannot be used
// with --profile until the user renames it to a custom name.
// If a file already exists at the destination, ErrExportConflict is returned
// so the caller can surface a clear message without silently overwriting edits.
func ExportEmbedded(name string) (string, error) {
	slug := strings.TrimSuffix(name, ".yaml")
	if !IsBuiltIn(slug) {
		return "", fmt.Errorf("%w: %q is not a built-in profile", ErrProfileNotFound, slug)
	}

	data, err := embeddedProfiles.ReadFile("data/" + slug + ".yaml")
	if err != nil {
		return "", fmt.Errorf("%w: read embedded %q: %v", ErrProfileNotFound, slug, err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	dir := filepath.Join(home, ".config", "gg", "profiles")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create profiles directory %q: %w", dir, err)
	}

	dest := filepath.Join(dir, slug+".yaml")
	if _, err := os.Stat(dest); err == nil {
		return dest, ErrExportConflict
	}

	if err := os.WriteFile(dest, data, 0644); err != nil {
		return "", fmt.Errorf("write profile %q: %w", dest, err)
	}
	return dest, nil
}

// LoadBuiltIn loads a profile exclusively from the embedded binary, bypassing
// the resolution hierarchy and collision guard. It is intended for tooling that
// always needs the canonical built-in definition (e.g. `gg profile list`,
// `gg profile view`). Use Load for the normal --profile flag path.
func LoadBuiltIn(name string) (*Profile, error) {
	slug := strings.TrimSuffix(name, ".yaml")
	return loadEmbedded(slug)
}

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

// durationPctTolerance is the maximum absolute deviation from 1.0 that the
// sum of all segment duration_pct values may have before validation rejects
// the profile.
const durationPctTolerance = 0.01

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
		// duration_pct == 0 is only meaningful for SegmentStep (instant
		// transition with no hold time). For all other types it would produce
		// a zero-duration segment and is almost certainly a mistake.
		if s.DurationPct == 0 && s.Type != SegmentStep {
			return fmt.Errorf("%w: %q: segment[%d] duration_pct must be > 0 for type %q (0 is only valid for \"step\")", ErrInvalidProfile, src, i, s.Type)
		}
		if s.RPSMultiplier < 0 || s.RPSMultiplier > 1 {
			return fmt.Errorf("%w: %q: segment[%d] rps_multiplier must be in [0,1]", ErrInvalidProfile, src, i)
		}
	}

	// Ensure the sum of all duration_pct values is close enough to 1.0 that
	// InflateSegments will produce a total duration matching the requested run
	// duration. Step segments with duration_pct == 0 contribute nothing to the
	// sum and are intentionally included in the total (they add 0).
	if sum := p.TotalNonZeroPct(); sum < 1.0-durationPctTolerance || sum > 1.0+durationPctTolerance {
		return fmt.Errorf("%w: %q: sum of segment duration_pct values is %.4f, must be within %.2f of 1.0", ErrInvalidProfile, src, sum, durationPctTolerance)
	}

	return nil
}
