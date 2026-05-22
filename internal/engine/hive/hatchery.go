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

// hatcheryTicksPerSec is the number of micro-batch ticks in one second.
// Derived from hatcheryTick so the two constants stay in sync.
const hatcheryTicksPerSec = int(time.Second / hatcheryTick) // 100

// ── hatchery ─────────────────────────────────────────────────────────────────

// hatchery is the Smooth Dispatcher. It reads SpawnManifests from the Queen
// and spreads the requested number of Actor goroutines evenly across the
// 1-second window using micro-batch ticks.
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

// dispatch spreads `manifest.Count` Actor goroutines evenly across one
// 1-second window using micro-batch ticks of hatcheryTick duration.
//
// Batching maths:
//
//	batchSize = count / hatcheryTicksPerSec        (floor — base per-tick count)
//	remainder = count % hatcheryTicksPerSec        (extra 1 per-tick for first N ticks)
//
// E.g. count=150: batchSize=1, remainder=50 → first 50 ticks spawn 2, next 50 spawn 1.
// E.g. count=10:  batchSize=0, remainder=10 → first 10 ticks spawn 1, remaining skip.
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

	specIdx := manifest.SpecIndex

	batchSize := count / hatcheryTicksPerSec
	remainder := count % hatcheryTicksPerSec

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
			spec := specs[specIdx%len(specs)]
			specIdx++

			h.e.activeActors.Add(1)
			capturedSpec := spec
			capturedShard := shard
			go func() {
				defer h.e.activeActors.Add(-1)
				_ = h.e.executeActor(ctx, capturedSpec, capturedShard)
			}()

			*spawnIdx++
			spawned++
		}
	}
}
