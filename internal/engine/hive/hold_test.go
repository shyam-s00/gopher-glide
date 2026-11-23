package hive

import (
	"context"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
)

func counts(ms []SpawnManifest) []int {
	out := make([]int, len(ms))
	for i, m := range ms {
		out[i] = m.Count
	}
	return out
}

func TestHold_Accessors(t *testing.T) {
	e := New()
	if e.GetHoldRPS() != 0 {
		t.Fatalf("new engine holds %d", e.GetHoldRPS())
	}
	e.HoldRPS(220)
	if e.GetHoldRPS() != 220 {
		t.Errorf("hold = %d", e.GetHoldRPS())
	}
	e.HoldRPS(-4)
	if e.GetHoldRPS() != 1 {
		t.Errorf("hold below 1 should clamp to 1, got %d", e.GetHoldRPS())
	}
	e.ReleaseHold()
	if e.GetHoldRPS() != 0 {
		t.Errorf("hold after release = %d", e.GetHoldRPS())
	}
}

func TestEffectiveRPS(t *testing.T) {
	e := New()
	e.ApplyBias(-500)
	if got := e.effectiveRPS(100); got != 1 {
		t.Errorf("scheduled+bias below 1 should floor at 1, got %v", got)
	}
	e.ApplyBias(+540) // bias 40
	if got := e.effectiveRPS(100); got != 140 {
		t.Errorf("scheduled+bias = %v, want 140", got)
	}
	e.HoldRPS(220)
	if got := e.effectiveRPS(100); got != 220 {
		t.Errorf("hold must replace schedule and bias, got %v", got)
	}
}

// A hold set during a ramp keeps the rate flat for the whole window loop.
func TestQueen_Hold_FlatAcrossRamp(t *testing.T) {
	t.Parallel()
	e := New()
	e.HoldRPS(220)
	e.ApplyBias(30) // bias must not leak into a held rate
	stages := []config.Stage{{Duration: 3 * time.Second, TargetRPS: 500}}
	ms := runQueenOn(t, e, stages, 6*time.Second)
	if len(ms) != 3 {
		t.Fatalf("manifests = %v", counts(ms))
	}
	for i, m := range ms {
		if m.Count != 220 {
			t.Errorf("window %d count = %d, want 220", i, m.Count)
		}
	}
	if got := e.targetRPS.Load(); got != 220 {
		t.Errorf("published target = %d, want 220", got)
	}
}

// The hold survives a stage boundary and an instant (zero-duration) step.
func TestQueen_Hold_SurvivesStageBoundaryAndInstantStep(t *testing.T) {
	t.Parallel()
	e := New()
	e.HoldRPS(80)
	stages := []config.Stage{
		{Duration: 2 * time.Second, TargetRPS: 100},
		{Duration: 0, TargetRPS: 900},
		{Duration: 2 * time.Second, TargetRPS: 500},
	}
	ms := runQueenOn(t, e, stages, 8*time.Second)
	if len(ms) != 5 {
		t.Fatalf("manifests = %v", counts(ms))
	}
	for i, m := range ms {
		if m.Count != 80 {
			t.Errorf("manifest %d count = %d, want 80 (all: %v)", i, m.Count, counts(ms))
		}
	}
}

// Release rejoins the schedule where the stage clock is now, and the run does
// not last longer than the profile.
func TestQueen_Hold_ReleaseRejoinsScheduleOnTime(t *testing.T) {
	t.Parallel()
	e := New()
	e.HoldRPS(50)
	time.AfterFunc(1500*time.Millisecond, e.ReleaseHold)

	stages := []config.Stage{{Duration: 4 * time.Second, TargetRPS: 400}}
	start := time.Now()
	ms := runQueenOn(t, e, stages, 8*time.Second)
	elapsed := time.Since(start)

	// Windows end at 1..4 s; the ramp reads 100, 200, 300, 400 at those points.
	want := []int{50, 50, 300, 400}
	if len(ms) != len(want) {
		t.Fatalf("manifests = %v, want %v", counts(ms), want)
	}
	for i, m := range ms {
		if m.Count != want[i] {
			t.Errorf("counts = %v, want %v", counts(ms), want)
			break
		}
	}
	if elapsed < 3800*time.Millisecond || elapsed > 4600*time.Millisecond {
		t.Errorf("run took %v, want about 4s", elapsed)
	}
}

// A hold placed after the run started takes effect at the next window.
func TestQueen_Hold_AppliedMidRun(t *testing.T) {
	t.Parallel()
	e := New()
	time.AfterFunc(1500*time.Millisecond, func() { e.HoldRPS(60) })
	stages := []config.Stage{{Duration: 3 * time.Second, TargetRPS: 300}}
	ms := runQueenOn(t, e, stages, 6*time.Second)
	want := []int{100, 200, 60}
	if got := counts(ms); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("counts = %v, want %v", got, want)
	}
}

// runQueenOn runs the queen on an existing engine so the test controls its hold/bias.
func runQueenOn(t *testing.T, e *Engine, stages []config.Stage, timeout time.Duration) []SpawnManifest {
	t.Helper()
	q := &queen{e: e}
	manifestCh := make(chan SpawnManifest, 64)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		err := q.run(ctx, stages, 1.0, manifestCh)
		close(manifestCh)
		done <- err
	}()
	var out []SpawnManifest
	for m := range manifestCh {
		out = append(out, m)
	}
	<-done
	return out
}
