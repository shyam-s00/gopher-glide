package hive

import "time"

// SpawnManifest is the message the Queen sends to the Hatchery for each
// dispatch window. It tells the Hatchery exactly how many Actors to spawn,
// over what Duration to spread them, and which spec index to start from.
//
// The Duration field is the authoritative window for the Hatchery's
// micro-batch math. It is set by the Queen to:
//   - time.Second for normal 1-second heartbeat ticks, and
//   - the fractional remainder for sub-second stage-end timers.
//
// This decouples the Hatchery from any fixed-clock assumption: it simply
// divides Count across the given Duration regardless of wall-clock length.
type SpawnManifest struct {
	// Count is the number of Actor goroutines to launch in this window.
	Count int
	// Duration is the time window over which Count actors should be spread.
	// The Hatchery uses this to calculate micro-batch sizes dynamically.
	Duration time.Duration
	// SpecIndex is the round-robin position in the RequestSpec slice that
	// the first Actor of this batch should use. Subsequent Actors in the
	// batch advance the index modulo len(specs).
	SpecIndex int
}
