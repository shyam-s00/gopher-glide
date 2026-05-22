package hive

import (
	"context"
	"math"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// ── queen ─────────────────────────────────────────────────────────────────────

// queen is the Simulation Scheduler. It runs on a fixed 1-second heartbeat
// ticker, evaluates the current stage position, interpolates (LERPs) the
// target RPS, applies any pending Director bias, and emits one SpawnManifest
// per tick onto manifestCh for the Hatchery to consume.
//
// The Queen owns:
//   - Stage iteration (including zero-duration step jumps)
//   - rpsBias accumulation from biasCh
//   - targetRPS / currentStage atomics
//   - round-robin spec index across all manifests
//
// The Queen does NOT own HTTP execution — that belongs to the Hatchery/Actor.
type queen struct {
	e *Engine // back-pointer to shared atomics and channels
}

// run is the Queen's main loop. It is launched as a goroutine by RunStages and
// returns when ctx is cancelled or all stages complete.
//
//   - stages is the ordered list of load stages from the config.
//   - timeScale compresses/expands stage durations (1.0 = real-time).
//   - specs is the request spec slice; the Queen advances a round-robin index
//     and embeds it in each SpawnManifest so the Hatchery knows which spec to use.
//   - manifestCh is written non-blocking; a full channel drops the manifest
//     (Hatchery is behind — acceptable under overload).
func (q *queen) run(
	ctx context.Context,
	stages []config.Stage,
	timeScale float64,
	specs []httpreader.RequestSpec,
	manifestCh chan<- SpawnManifest,
) error {
	if timeScale <= 0 {
		timeScale = 1.0
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	specIdx := 0 // global round-robin position advanced by every manifest
	prevRPS := 0 // RPS at the end of the previous stage (starts at 0)

	for stageIdx, stage := range stages {
		// ── Zero-duration stage: instant step, no tick ─────────────────────
		// Emit at most one manifest for the step, update state, then
		// immediately advance to the next stage without waiting for a tick.
		if stage.Duration == 0 {
			prevRPS = stage.TargetRPS
			q.e.targetRPS.Store(int64(stage.TargetRPS))
			q.e.currentStage.Store(int32(stageIdx))

			rps := stage.TargetRPS
			if rps < 1 {
				rps = 1
			}
			// Drain any pending bias (keeps the atomic consistent).
			q.drainBias()
			biasedRPS := rps + int(q.e.rpsBias.Load())
			if biasedRPS < 1 {
				biasedRPS = 1
			}
			q.e.targetRPS.Store(int64(biasedRPS))

			select {
			case manifestCh <- SpawnManifest{Count: biasedRPS, SpecIndex: specIdx % len(specs)}:
				specIdx += biasedRPS
			default:
			}
			continue
		}

		// ── Normal (timed) stage ───────────────────────────────────────────
		scaledDur := time.Duration(float64(stage.Duration) / timeScale)
		q.e.currentStage.Store(int32(stageIdx))

		stageStart := time.Now()
		stageEnd := stageStart.Add(scaledDur)

		startRPS := float64(prevRPS)
		endRPS := float64(stage.TargetRPS)

		// stageTimer fires when the stage's wall-clock duration elapses.
		// Without it the Queen blocks on ticker.C for up to 1 second even
		// after the stage has already ended — wrong for sub-second stages.
		stageTimer := time.NewTimer(scaledDur)

		// emitAt is a small helper that evaluates the LERP at a given
		// instant, applies bias, and emits a SpawnManifest non-blocking.
		emitAt := func(now time.Time) {
			q.drainBias()
			elapsed := now.Sub(stageStart)
			pct := float64(elapsed) / float64(scaledDur)
			if pct > 1 {
				pct = 1
			} else if pct < 0 {
				pct = 0
			}
			currentRPS := startRPS + (endRPS-startRPS)*pct
			biasedRPS := currentRPS + float64(q.e.rpsBias.Load())
			if biasedRPS < 1 {
				biasedRPS = 1
			}
			roundedRPS := int(math.Round(biasedRPS))
			q.e.targetRPS.Store(int64(roundedRPS))
			select {
			case manifestCh <- SpawnManifest{Count: roundedRPS, SpecIndex: specIdx % len(specs)}:
				specIdx += roundedRPS
			default:
				// Hatchery is behind — drop this manifest silently.
			}
		}

	stageLoop:
		for {
			select {
			case <-ctx.Done():
				stageTimer.Stop()
				return nil

			case <-stageTimer.C:
				// Stage duration elapsed. Emit one final manifest at endRPS
				// (pct=1) so every stage — including sub-second ones that
				// never see a 1-second heartbeat tick — dispatches actors.
				emitAt(stageEnd)
				break stageLoop

			case now := <-ticker.C:
				if now.After(stageEnd) {
					// Tick arrived after stage end (race between timer and
					// ticker). The stageTimer case handles the final emit.
					break stageLoop
				}
				emitAt(now)
			}
		}
		stageTimer.Stop()

		prevRPS = stage.TargetRPS
	}
	return nil
}

// drainBias reads all pending deltas from biasCh and accumulates them into the
// rpsBias atomic. Non-blocking — returns immediately when the channel is empty.
func (q *queen) drainBias() {
drainLoop:
	for {
		select {
		case delta := <-q.e.biasCh:
			q.e.rpsBias.Add(int64(delta))
		default:
			break drainLoop
		}
	}
}
