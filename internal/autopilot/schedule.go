package autopilot

import (
	"math"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
)

// ScheduleAt is the rate the stage plan calls for elapsed into the run, without
// bias or hold. It mirrors the engine's interpolation closely enough to tell a
// guard how far to release; it ends on the last stage's rate.
func ScheduleAt(stages []config.Stage, timeScale float64, elapsed time.Duration) int {
	if timeScale <= 0 {
		timeScale = 1
	}
	prev := 0
	for _, s := range stages {
		dur := time.Duration(float64(s.Duration) / timeScale)
		if dur <= 0 {
			prev = s.TargetRPS
			continue
		}
		if elapsed < dur {
			pct := float64(elapsed) / float64(dur)
			return int(math.Round(float64(prev) + float64(s.TargetRPS-prev)*pct))
		}
		elapsed -= dur
		prev = s.TargetRPS
	}
	return prev
}
