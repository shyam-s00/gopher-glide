package profile

import "time"

// SegmentType describes how traffic is shaped over a segment's duration.
type SegmentType string

const (
	// SegmentFlat holds RPS constant at rps_multiplier * peak for the full duration.
	// No transition — the RPS stays exactly where the previous segment left it
	// (or at rps_multiplier * peak if it is the first segment).
	SegmentFlat SegmentType = "flat"

	// SegmentStep makes an instant 0-duration jump to rps_multiplier * peak,
	// then holds at that level for duration_pct * total. When duration_pct is 0
	// the segment represents a pure instant transition with no hold time.
	SegmentStep SegmentType = "step"

	// SegmentLinear linearly interpolates (LERP) from the previous RPS level to
	// rps_multiplier * peak over duration_pct * total.
	SegmentLinear SegmentType = "linear"

	// SegmentExponential approximates an exponential curve from the previous RPS
	// level to rps_multiplier * peak over duration_pct * total. The inflater
	// decomposes this into several short linear stages.
	SegmentExponential SegmentType = "exponential"
)

// Segment is one abstract traffic shape within a Profile.
// It does not contain absolute values — they are derived at inflation time.
type Segment struct {
	// Type controls the shape of this segment.
	Type SegmentType `yaml:"type"`

	// DurationPct is the fraction of the total run time allocated to this
	// segment. Values must be in [0, 1]. The sum of all non-zero DurationPct
	// values in a profile should equal 1.0. A value of 0 is valid for SegmentStep
	// and represents a pure instant transition with no hold time.
	DurationPct float64 `yaml:"duration_pct"`

	// RPSMultiplier is the target RPS for this segment expressed as a fraction
	// of the peak RPS. Values must be in [0, 1]: 0.0 = zero traffic, 1.0 = peak.
	RPSMultiplier float64 `yaml:"rps_multiplier"`
}

// ConfigOverride carries optional per-profile config knobs that are applied on
// top of the loaded config when the profile is active. Only non-zero values are
// applied; zero means "no override".
type ConfigOverride struct {
	// Jitter overrides the organic RPS noise setting. 0.4 = ±40% per tick.
	Jitter float64 `yaml:"jitter"`
}

// Profile is the parsed representation of a profile YAML file.
// It defines a traffic shape abstractly so it can be scaled to any peak RPS
// and total duration at inflation time.
type Profile struct {
	// Name is the canonical profile identifier, e.g. "flash-sale".
	Name string `yaml:"name"`

	// Description is a one-line human-readable summary shown in `gg profile list`.
	Description string `yaml:"description"`

	// DefaultDuration is the reference total run time used when the caller does
	// not supply --duration. Stored as a Go duration string, e.g. "3m".
	DefaultDuration time.Duration `yaml:"default_duration"`

	// DefaultPeakRPS is the reference peak RPS used when the caller does not
	// supply --peak-rps.
	DefaultPeakRPS int `yaml:"default_peak_rps"`

	// ConfigOverride holds optional config-level knobs (e.g. jitter) baked into
	// the profile. Applied before CLI flags so the flag always wins.
	ConfigOverride ConfigOverride `yaml:"config"`

	// Segments is the ordered list of abstract traffic shape descriptors.
	Segments []Segment `yaml:"segments"`
}

// TotalNonZeroPct returns the sum of all non-zero DurationPct values.
// A well-formed profile should return a value very close to 1.0.
func (p *Profile) TotalNonZeroPct() float64 {
	var total float64
	for _, s := range p.Segments {
		total += s.DurationPct
	}
	return total
}
