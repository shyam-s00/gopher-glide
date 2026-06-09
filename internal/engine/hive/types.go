package hive

import "time"

// SpawnManifest is the message the Queen sends to the Hatchery for each
// dispatch window. It tells the Hatchery exactly how many Actors to spawn
// and over what Duration to spread them.
//
// The Duration field is the authoritative window for the Hatchery's
// micro-batch math. It is set by the Queen to:
//   - time.Second for normal 1-second heartbeat ticks, and
//   - the fractional remainder for sub-second stage-end timers.
//
// This decouples the Hatchery from any fixed-clock assumption: it simply
// divides Count across the given Duration regardless of wall-clock length.
//
// Journey selection is the Hatchery's responsibility: it round-robins
// through the parsed []Journey slice using its own monotonic spawn counter
// (journeyIdx = spawnIdx % len(journeys)), so no per-manifest index needs to
// travel from the Queen.
type SpawnManifest struct {
	// Count is the number of Actor goroutines to launch in this window.
	Count int
	// Duration is the time window over which Count actors should be spread.
	// The Hatchery uses this to calculate micro-batch sizes dynamically.
	Duration time.Duration
}
