package hive

import (
	"context"
	"math"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
)

// queen is the Simulation Scheduler. It operates on a proactive emit-then-sleep
// loop rather than a reactive ticker:
//
//  1. Compute the next window: up to 1 second ahead, capped at the stage boundary.
//  2. Emit one SpawnManifest whose Count is proportionally scaled to the window
//     duration (e.g. 1000 actors for a 0.5 s window at 2000 RPS, not 2000).
//  3. Sleep for exactly that window duration (or return on context cancel).
//  4. Advance windowStart and repeat until the stage ends.
//
// This three-step design eliminates three classes of stage-transition artefacts:
//   - RC1 Lagging 1 s dip: no detached global ticker means stages start
//     dispatching immediately with zero idle gap.
//   - RC2 Overlapping manifests: a single code path emits manifests — there is
//     no race between a ticker case and a stageTimer case at boundaries.
//   - RC3 Full-count burst: Count is always scaled to windowDur / 1 s, so a
//     10 ms fractional window never receives a full second's worth of actors.
type queen struct {
	e *Engine // back-pointer to shared atomics and channels
}

// run is the Queen's main loop. It is launched as a goroutine by RunStages and
// returns when ctx is cancelled or all stages complete.
func (q *queen) run(
	ctx context.Context,
	stages []config.Stage,
	timeScale float64,
	manifestCh chan<- SpawnManifest,
) error {
	if timeScale <= 0 {
		timeScale = 1.0
	}

	prevRPS := 0 // RPS at the end of the previous stage (starts at 0)

	for stageIdx, stage := range stages {
		// Zero-duration stage: instant step, no window loop.
		if stage.Duration == 0 {
			prevRPS = stage.TargetRPS
			q.e.currentStage.Store(int32(stageIdx))

			rps := stage.TargetRPS
			if rps < 1 {
				rps = 1
			}
			q.drainBias()
			biasedRPS := rps + int(q.e.rpsBias.Load())
			if biasedRPS < 1 {
				biasedRPS = 1
			}
			q.e.targetRPS.Store(int64(biasedRPS))

			select {
			case manifestCh <- SpawnManifest{Count: biasedRPS, Duration: time.Second}:
			default:
			}
			continue
		}

		// Normal (timed) stage: proactive emit-then-sleep loop. The window
		// boundaries are anchored to the fixed stageStart time, not
		// to time.Now() after each sleep, so OS scheduler jitter never
		// accumulates into drift across a long stage.
		scaledDur := time.Duration(float64(stage.Duration) / timeScale)
		q.e.currentStage.Store(int32(stageIdx))

		stageStart := time.Now()
		stageEnd := stageStart.Add(scaledDur)

		startRPS := float64(prevRPS)
		endRPS := float64(stage.TargetRPS)

		windowStart := stageStart

		for windowStart.Before(stageEnd) {
			// 1. Compute the window.
			windowEnd := windowStart.Add(time.Second)
			if windowEnd.After(stageEnd) {
				windowEnd = stageEnd
			}
			windowDur := windowEnd.Sub(windowStart)

			// Skip windows shorter than one Hatchery tick — nothing meaningful
			// can be dispatched and a divide-by-zero is possible in the Hatchery.
			if windowDur < hatcheryTick {
				break
			}

			// 2. LERP + bias + proportional count. Evaluate the LERP at
			// windowEnd (the end of the upcoming window) so the ramp
			// progresses smoothly over the stage.
			elapsed := windowEnd.Sub(stageStart)
			pct := float64(elapsed) / float64(scaledDur)
			if pct > 1 {
				pct = 1
			} else if pct < 0 {
				pct = 0
			}

			q.drainBias()
			currentRPS := startRPS + (endRPS-startRPS)*pct
			biasedRPS := currentRPS + float64(q.e.rpsBias.Load())
			if biasedRPS < 1 {
				biasedRPS = 1
			}

			// fullSecondRPS is what the TUI shows as "target RPS".
			// count is scaled to the actual window so the Hatchery spawns the
			// right number of actors regardless of how long the window is.
			fullSecondRPS := int(math.Round(biasedRPS))
			count := int(math.Round(biasedRPS * float64(windowDur) / float64(time.Second)))
			if count < 1 {
				count = 1
			}

			q.e.targetRPS.Store(int64(fullSecondRPS))

			// 3. Emit manifest for the upcoming window.
			select {
			case manifestCh <- SpawnManifest{Count: count, Duration: windowDur}:
			default:
				// Hatchery is behind — drop this manifest silently.
			}

			// 4. Sleep for the window duration. Emit first so the Hatchery is
			// already dispatching while the Queen waits; a timer + select
			// (not time.Sleep) keeps cancellation responsive. Draining
			// biasCh once the timer fires folds mid-sleep bias into the next
			// window's LERP.
			timer := time.NewTimer(windowDur)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
				q.drainBias()
			}

			// Advance to the next window using the computed boundary (not
			// time.Now()) to prevent jitter from accumulating into drift.
			windowStart = windowEnd
		}

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
