package hive

// SpawnManifest is the message the Queen sends to the Hatchery on each
// 1-second heartbeat tick. It tells the Hatchery how many Actors to spawn
// and which spec (by index into the specs slice) they should execute.
type SpawnManifest struct {
	// Count is the number of Actor goroutines to launch this second.
	Count int
	// SpecIndex is the round-robin position in the RequestSpec slice that
	// the first Actor of this batch should use. Subsequent Actors in the
	// batch advance the index modulo len(specs).
	SpecIndex int
}
