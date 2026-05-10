package profile

import (
	"math"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
)

const (
	// expSubSteps is the number of linear sub-stages used to approximate the
	// exponential curve. Higher values produce a smoother curve at the cost of
	// more stage entries.
	expSubSteps = 8

	// expSteepness controls the aggressiveness of the exponential bend.
	// 2.5 produces a clearly visible exponential ramp without overshooting.
	expSteepness = 2.5
)

// InflateSegments compiles the abstract profile segments into a concrete
// []config.Stage slice that the engine can execute directly.
//
// peak is the desired peak RPS (typically from --peak-rps or prof.DefaultPeakRPS).
// dur is the total desired run duration (from --duration or prof.DefaultDuration).
//
// Segment type behaviours:
//
//	flat        — snap to targetRPS if not already there, then hold for the
//	              full segment duration (no lerp when already at level).
//	step        — emit an instant 0-duration jump to targetRPS, then hold for
//	              the segment duration (0 duration_pct = pure instant transition).
//	linear      — single stage that lerps from prevRPS to targetRPS over duration.
//	exponential — decomposed into expSubSteps linear sub-stages that approximate
//	              an exponential curve from prevRPS to targetRPS.
func InflateSegments(prof *Profile, peak int, dur time.Duration) []config.Stage {
	var stages []config.Stage
	prevRPS := 0

	for _, seg := range prof.Segments {
		segDur := time.Duration(float64(dur) * seg.DurationPct)
		targetRPS := int(math.Round(float64(peak) * seg.RPSMultiplier))

		switch seg.Type {
		case SegmentStep:
			// Always emit an instant 0-duration stage (the "step").
			stages = append(stages, config.Stage{Duration: 0, TargetRPS: targetRPS})
			// If a hold is requested, emit a sustain stage at the same level.
			if segDur > 0 {
				stages = append(stages, config.Stage{Duration: segDur, TargetRPS: targetRPS})
			}

		case SegmentFlat:
			// If we are not already at the target level, snap there instantly.
			if prevRPS != targetRPS {
				stages = append(stages, config.Stage{Duration: 0, TargetRPS: targetRPS})
			}
			// Hold at the level for the full segment duration.
			if segDur > 0 {
				stages = append(stages, config.Stage{Duration: segDur, TargetRPS: targetRPS})
			}

		case SegmentLinear:
			// Single stage — the engine lerps from prevRPS to targetRPS naturally.
			if segDur > 0 {
				stages = append(stages, config.Stage{Duration: segDur, TargetRPS: targetRPS})
			}

		case SegmentExponential:
			sub := inflateExponential(prevRPS, targetRPS, segDur, expSubSteps, expSteepness)
			stages = append(stages, sub...)
		}

		prevRPS = targetRPS
	}

	return stages
}

// inflateExponential decomposes an exponential segment into n linear sub-stages.
// The RPS at the boundary of each sub-step follows:
//
//	rps(t) = fromRPS + Δ * (e^(α·t) − 1) / (e^α − 1)
//
// where t ∈ [0,1] is the fractional position within the segment and α is the
// steepness parameter.
func inflateExponential(fromRPS, toRPS int, dur time.Duration, n int, steepness float64) []config.Stage {
	if n <= 0 {
		n = expSubSteps
	}
	subDur := dur / time.Duration(n)
	if subDur == 0 {
		// Duration too short to decompose — emit a single instant step.
		return []config.Stage{{Duration: 0, TargetRPS: toRPS}}
	}

	stages := make([]config.Stage, 0, n)
	delta := float64(toRPS - fromRPS)
	denom := math.Exp(steepness) - 1

	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		var rps int
		if denom == 0 {
			// Degenerate case (steepness == 0) — fall back to linear.
			rps = fromRPS + int(math.Round(delta*t))
		} else {
			rps = fromRPS + int(math.Round(delta*(math.Exp(steepness*t)-1)/denom))
		}
		stages = append(stages, config.Stage{Duration: subDur, TargetRPS: rps})
	}
	return stages
}
