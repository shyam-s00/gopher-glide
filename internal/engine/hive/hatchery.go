package hive

import (
	"context"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// ── Hatchery constants ────────────────────────────────────────────────────────

// hatcheryTick is the micro-batch dispatch interval.
//
// Using a fixed 10 ms tick rather than per-Actor sleep sidesteps the OS
// scheduler resolution floor (~1 ms on Linux/macOS). A naive per-Actor
// time.Sleep at high RPS would silently cap throughput at ~1 000 RPS
// regardless of target. The Hatchery instead wakes once every 10 ms and
// spawns a batch in a tight loop with no sleep between individual goroutines.
const hatcheryTick = 10 * time.Millisecond

// ── hatchery ─────────────────────────────────────────────────────────────────

// hatchery is the Smooth Dispatcher. It reads SpawnManifests from the Queen
// and spreads the requested number of Actor goroutines evenly across the
// manifest's Duration window using micro-batch ticks.
//
// Design invariants:
//   - activeActors is incremented before each go-launch and decremented by
//     defer inside the goroutine closure — so GetMetrics always sees a
//     non-negative, accurate in-flight count.
//   - The shard index for each Actor is determined by a monotonically
//     increasing spawn counter (spawnIdx % numShards) — no rand locking.
//   - Context cancellation is checked on every tick; the dispatch loop exits
//     cleanly without spawning further goroutines.
type hatchery struct {
	e *Engine // back-pointer to shared Engine state
}

// run reads SpawnManifests from manifestCh and dispatches Actors for each.
//
// It blocks until ctx is cancelled or manifestCh is closed. It is launched
// as a goroutine by RunStages and must return promptly on context cancellation.
func (h *hatchery) run(
	ctx context.Context,
	manifestCh <-chan SpawnManifest,
	specs []httpreader.RequestSpec,
) error {
	spawnIdx := 0 // monotonically increasing; drives shard assignment
	for {
		select {
		case <-ctx.Done():
			return nil
		case manifest, ok := <-manifestCh:
			if !ok {
				return nil
			}
			h.dispatch(ctx, manifest, specs, &spawnIdx)
		}
	}
}

// dispatch spreads `manifest.Count` Actor goroutines evenly across the
// window defined by `manifest.Duration` using micro-batch ticks of hatcheryTick.
//
// Batching maths:
//
//	numTicks  = max(1, manifest.Duration / hatcheryTick)  (ticks that fit in the window)
//	batchSize  = count / numTicks                          (floor — base per-tick count)
//	remainder  = count % numTicks                          (extra 1 per-tick for first N ticks)
//
// E.g. count=150, duration=1s (100 ticks): batchSize=1, remainder=50 → first 50 ticks spawn 2.
// E.g. count=5,   duration=400ms (40 ticks): batchSize=0, remainder=5 → first 5 ticks spawn 1.
// E.g. count=10,  duration=15ms  (2 ticks):  batchSize=5, remainder=0 → each tick spawns 5.
//
// spawnIdx is shared across calls so shard assignment is globally monotonic.
func (h *hatchery) dispatch(
	ctx context.Context,
	manifest SpawnManifest,
	specs []httpreader.RequestSpec,
	spawnIdx *int,
) {
	count := manifest.Count
	if count <= 0 || len(specs) == 0 {
		return
	}

	// Compute the number of micro-batch ticks that fit within the manifest
	// window. A minimum of 1 tick prevents divide-by-zero for tiny durations.
	window := manifest.Duration
	if window <= 0 {
		window = time.Second // safe default: fall back to 1-second window
	}
	numTicks := int(window / hatcheryTick)
	if numTicks < 1 {
		numTicks = 1
	}

	batchSize := count / numTicks
	remainder := count % numTicks

	ticker := time.NewTicker(hatcheryTick)
	defer ticker.Stop()

	spawned := 0
	for tick := 0; spawned < count; tick++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// Distribute remainder across the first `remainder` ticks so
		// spawning is as even as possible without fractional actors.
		batch := batchSize
		if tick < remainder {
			batch++
		}

		for i := 0; i < batch && spawned < count; i++ {
			shard := *spawnIdx % numShards

			// Each Actor goroutine executes the entire Journey (all specs
			// in order) with its own private ActorMemory.  Variables
			// extracted from step N are automatically injected into step N+1.
			// A single-spec journey is behaviourally identical to the old
			// stateless single-request model.
			h.e.activeActors.Add(1)
			capturedShard := shard
			go func() {
				defer h.e.activeActors.Add(-1)
				_ = h.e.executeJourney(ctx, specs, capturedShard)
			}()

			*spawnIdx++
			spawned++
		}
	}
}
