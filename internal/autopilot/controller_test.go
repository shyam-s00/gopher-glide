package autopilot

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
)

func findParams() Params {
	return Params{Mode: ModeFindCeiling, StartRPS: 20, StepRPS: 15, MaxRPS: 400, MaxP99Ms: 200,
		MaxErrorRate: 0.01, StepInterval: 5 * time.Second, ConfirmHold: 10 * time.Second,
		MaxDuration: 10 * time.Minute, MinSamples: 50, Settle: time.Second}
}

func TestFindCeiling_StepsBreachesBacksOffAndConfirms(t *testing.T) {
	f := newFake(t0, 20, 100)
	rec := &recorder{}
	c := mustNew(t, findParams(), f, rec, nil)
	c.Start(t0)
	f.mark()
	drive(c, f, 300)

	res := c.Finalize(false)
	if res.Outcome != OutcomeCeilingFound || res.CeilingRPS != 95 {
		t.Fatalf("outcome=%s ceiling=%d, want ceiling_found at 95; holds=%v", res.Outcome, res.CeilingRPS, f.holds)
	}
	if want := []int{20, 35, 50, 65, 80, 95, 110, 95}; !slices.Equal(f.holds, want) {
		t.Fatalf("holds = %v, want %v", f.holds, want)
	}
	if rec.stopped != 1 {
		t.Fatalf("StopRun called %d times, want 1", rec.stopped)
	}
	wantStates := []State{StateStepping, StateStepping, StateStepping, StateStepping, StateStepping, StateStepping,
		StateStepping, StateBreached, StateConfirming, StateDone}
	if got := rec.states(); !slices.Equal(got, wantStates) {
		t.Fatalf("states = %v, want %v", got, wantStates)
	}
	if !rec.hasMark("ceiling breached at 110 RPS") || !rec.hasMark("backing off to 95 RPS") || !rec.hasMark("ceiling confirmed at 95") {
		t.Fatalf("marks missing: %q", rec.marks)
	}
}

func TestFindCeiling_BreachMarkNamesTopFailingEndpoint(t *testing.T) {
	f := newFake(t0, 20, 50)
	f.top = []engine.EndpointError{{Endpoint: "POST /checkout", N: 40, Code: 503, Kind: "http_5xx"}}
	rec := &recorder{}
	c := mustNew(t, findParams(), f, rec, nil)
	c.Start(t0)
	drive(c, f, 120)
	if !rec.hasMark("top failing: POST /checkout 503") {
		t.Fatalf("breach mark lacks endpoint evidence: %q", rec.marks)
	}
}

func TestFindCeiling_ThinWindowExtendsTheStep(t *testing.T) {
	f := newFake(t0, 20, 1000)
	f.perSec = 5 // 50 samples needs 10s of data
	rec := &recorder{}
	c := mustNew(t, findParams(), f, rec, nil)
	c.Start(t0)
	f.mark()
	drive(c, f, 9) // past the 5s step interval, but only ~35 samples
	if len(f.holds) != 1 {
		t.Fatalf("stepped on a thin window: holds=%v", f.holds)
	}
	drive(c, f, 4)
	if len(f.holds) < 2 {
		t.Fatalf("did not step once 50 samples existed: holds=%v", f.holds)
	}
}

func TestFindCeiling_ThinWindowNeverCountsAsABreach(t *testing.T) {
	f := newFake(t0, 20, 1000)
	f.perSec = 1
	f.override = &engine.HealthWindow{Seconds: 5, P99Ms: 900, ErrorRate: 0.5, Samples: 5}
	rec := &recorder{}
	c := mustNew(t, findParams(), f, rec, nil)
	c.Start(t0)
	drive(c, f, 20)
	if rec.hasMark("breached") {
		t.Fatalf("breach declared on 5 samples: %q", rec.marks)
	}
}

func TestFindCeiling_NoCompletionsEndsUnstable(t *testing.T) {
	f := newFake(t0, 20, 1000)
	f.override = &engine.HealthWindow{Seconds: 5}
	rec := &recorder{}
	c := mustNew(t, findParams(), f, rec, nil)
	c.Start(t0)
	drive(c, f, 600)
	res := c.Finalize(false)
	if res.Outcome != OutcomeUnstable {
		t.Fatalf("outcome = %s, want unstable; marks=%q", res.Outcome, rec.marks)
	}
	if !rec.hasMark("no completed requests") {
		t.Fatalf("reason missing: %q", rec.marks)
	}
}

func TestFindCeiling_FailedConfirmationStepsDown(t *testing.T) {
	f := newFake(t0, 20, 100)
	rec := &recorder{}
	c := mustNew(t, findParams(), f, rec, nil)
	c.Start(t0)
	f.mark()
	// The service degrades once the controller has found its first breach.
	driveUntil(t, c, f, 200, func() bool { return slices.Contains(rec.states(), StateConfirming) })
	f.knee = func() int { return 70 }
	drive(c, f, 300)

	res := c.Finalize(false)
	if res.Outcome != OutcomeCeilingFound || res.CeilingRPS != 80-15 {
		t.Fatalf("outcome=%s ceiling=%d holds=%v", res.Outcome, res.CeilingRPS, f.holds)
	}
	if !rec.hasMark("still unhealthy at 95 RPS") {
		t.Fatalf("step-down mark missing: %q", rec.marks)
	}
}

func TestFindCeiling_UnstableAfterThreeConfirmations(t *testing.T) {
	f := newFake(t0, 20, 100)
	rec := &recorder{}
	c := mustNew(t, findParams(), f, rec, nil)
	c.Start(t0)
	f.mark()
	driveUntil(t, c, f, 200, func() bool { return slices.Contains(rec.states(), StateConfirming) })
	f.knee = func() int { return 5 }
	drive(c, f, 300)

	res := c.Finalize(false)
	if res.Outcome != OutcomeUnstable {
		t.Fatalf("outcome = %s, want unstable; holds=%v", res.Outcome, f.holds)
	}
	confirms := 0
	for _, s := range rec.states() {
		if s == StateConfirming {
			confirms++
		}
	}
	if confirms != maxConfirmAttempts {
		t.Fatalf("confirmation attempts = %d, want %d (holds %v)", confirms, maxConfirmAttempts, f.holds)
	}
}

func TestFindCeiling_NoHealthyRateBelowStartStepsDown(t *testing.T) {
	f := newFake(t0, 20, 10)
	rec := &recorder{}
	c := mustNew(t, findParams(), f, rec, nil)
	c.Start(t0)
	f.mark()
	drive(c, f, 300)
	if f.holds[1] != 5 {
		t.Fatalf("back-off from a breach at the start rate = %v, want 20 -> 5", f.holds)
	}
	if res := c.Finalize(false); res.Outcome != OutcomeCeilingFound || res.CeilingRPS != 5 {
		t.Fatalf("outcome=%s ceiling=%d", res.Outcome, res.CeilingRPS)
	}
}

func TestFindCeiling_NotReachedAtRPSCap(t *testing.T) {
	p := findParams()
	p.MaxRPS = 60
	f := newFake(t0, 20, 10000)
	rec := &recorder{}
	c := mustNew(t, p, f, rec, nil)
	c.Start(t0)
	f.mark()
	drive(c, f, 300)
	res := c.Finalize(false)
	if res.Outcome != OutcomeCeilingNotReached || res.CeilingRPS != 60 {
		t.Fatalf("outcome=%s ceiling=%d holds=%v", res.Outcome, res.CeilingRPS, f.holds)
	}
	if f.holds[len(f.holds)-1] > 60 {
		t.Fatalf("held above the cap: %v", f.holds)
	}
}

func TestFindCeiling_NotReachedAtMaxDuration(t *testing.T) {
	p := findParams()
	p.MaxDuration = 30 * time.Second
	f := newFake(t0, 20, 10000)
	rec := &recorder{}
	c := mustNew(t, p, f, rec, nil)
	c.Start(t0)
	f.mark()
	drive(c, f, 120)
	res := c.Finalize(false)
	if res.Outcome != OutcomeCeilingNotReached || res.CeilingRPS < 20 {
		t.Fatalf("outcome=%s ceiling=%d", res.Outcome, res.CeilingRPS)
	}
}

func TestFindCeiling_PercentStepsGrowAtLeastOne(t *testing.T) {
	p := findParams()
	p.StepRPS, p.StepPct, p.StartRPS = 0, 25, 2
	f := newFake(t0, 2, 10000)
	c := mustNew(t, p, f, &recorder{}, nil)
	c.Start(t0)
	f.mark()
	drive(c, f, 40)
	if want := []int{2, 3, 4, 5, 7, 9, 12, 15}; !slices.Equal(f.holds[:len(want)], want) {
		t.Fatalf("holds = %v, want prefix %v", f.holds, want)
	}
}

func TestFinalize_OutcomesWhenCutOff(t *testing.T) {
	tests := []struct {
		name     string
		mode     Mode
		runEnded bool
		drive    int
		want     Outcome
	}{
		{"find stopped early", ModeFindCeiling, false, 3, OutcomeStopped},
		{"find run ended while healthy", ModeFindCeiling, true, 30, OutcomeCeilingNotReached},
		{"find run ended before any healthy step", ModeFindCeiling, true, 1, OutcomeStopped},
		{"guard stopped", ModeSLOGuard, false, 3, OutcomeStopped},
		{"guard run ended", ModeSLOGuard, true, 3, OutcomeCompleted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := findParams()
			p.Mode = tc.mode
			p.RecoverHold = 5 * time.Second
			f := newFake(t0, 20, 1000)
			rec := &recorder{}
			c := mustNew(t, p, f, rec, nil)
			c.Start(t0)
			f.mark()
			drive(c, f, tc.drive)
			res := c.Finalize(tc.runEnded)
			if res.Outcome != tc.want {
				t.Fatalf("outcome = %s, want %s", res.Outcome, tc.want)
			}
			if res.Closing == nil || res.Closing.State != StateDone || res.Closing.Outcome != tc.want {
				t.Fatalf("closing event = %+v", res.Closing)
			}
			if last := res.Events[len(res.Events)-1]; last.Outcome != tc.want {
				t.Fatalf("history does not end with the outcome: %+v", last)
			}
		})
	}
}

func TestFinalize_AfterDoneDoesNotEmitAgain(t *testing.T) {
	p := findParams()
	p.MaxRPS = 20
	f := newFake(t0, 20, 1000)
	rec := &recorder{}
	c := mustNew(t, p, f, rec, nil)
	c.Start(t0)
	f.mark()
	drive(c, f, 60)
	res := c.Finalize(true)
	if res.Closing != nil || res.Outcome != OutcomeCeilingNotReached {
		t.Fatalf("closing=%+v outcome=%s", res.Closing, res.Outcome)
	}
}

func guardParams() Params {
	return Params{Mode: ModeSLOGuard, MaxP99Ms: 200, MaxErrorRate: 0.01, MinSamples: 50,
		StepInterval: 3 * time.Second, RecoverHold: 4 * time.Second, Settle: time.Second}
}

func staticSchedule(f *fakeTarget) func() (int, bool) {
	return func() (int, bool) { return f.schedule, true }
}

func TestGuard_NeedsTwoConsecutiveBreaches(t *testing.T) {
	f := newFake(t0, 200, 150)
	rec := &recorder{}
	c := mustNew(t, guardParams(), f, rec, staticSchedule(f))
	c.Start(t0)
	// One bad tick, then the target recovers on its own.
	f.now = f.now.Add(time.Second)
	c.Tick(f.now)
	f.knee = func() int { return 1000 }
	drive(c, f, 20)
	if len(f.holds) != 0 {
		t.Fatalf("clamped on a single breach or a recovered one: holds=%v", f.holds)
	}
}

func TestGuard_ClampsAtSeventyPercentOfActualRate(t *testing.T) {
	f := newFake(t0, 200, 150)
	rec := &recorder{}
	c := mustNew(t, guardParams(), f, rec, staticSchedule(f))
	c.Start(t0)
	driveUntil(t, c, f, 30, func() bool { return len(f.holds) > 0 })
	if len(f.holds) != 1 || f.holds[0] != 140 {
		t.Fatalf("holds = %v, want one clamp at 140", f.holds)
	}
	if !rec.hasMark("SLO Guard triggered") || !rec.hasMark("clamping 200 to 140") {
		t.Fatalf("clamp mark missing: %q", rec.marks)
	}
	if st := rec.states(); st[len(st)-1] != StateClamped {
		t.Fatalf("states = %v", st)
	}
	if mode, state := c.Status(); mode != ModeSLOGuard || state != StateClamped {
		t.Fatalf("status = %s/%s", mode, state)
	}
}

func TestGuard_RecoveryNeedsMarginNotJustUnderTheLine(t *testing.T) {
	f := newFake(t0, 200, 150)
	rec := &recorder{}
	c := mustNew(t, guardParams(), f, rec, staticSchedule(f))
	c.Start(t0)
	driveUntil(t, c, f, 30, func() bool { return len(f.holds) > 0 })
	// Under the 200ms line but over 80% of it: not good enough to release.
	f.override = &engine.HealthWindow{Seconds: 5, P99Ms: 170, Samples: 500}
	drive(c, f, 30)
	if f.releases != 0 || len(f.holds) != 1 {
		t.Fatalf("released without margin: holds=%v releases=%d", f.holds, f.releases)
	}
	f.override = &engine.HealthWindow{Seconds: 5, P99Ms: 100, Samples: 500}
	driveUntil(t, c, f, 30, func() bool { return len(f.holds) > 1 })
	if len(f.holds) < 2 || f.holds[1] <= f.holds[0] {
		t.Fatalf("did not start releasing with margin: holds=%v", f.holds)
	}
}

func TestGuard_SteppedReleaseThenLetGo(t *testing.T) {
	f := newFake(t0, 200, 1000)
	rec := &recorder{}
	c := mustNew(t, guardParams(), f, rec, staticSchedule(f))
	c.Start(t0)
	// Force the first trip, then let the target be healthy at any rate.
	f.override = &engine.HealthWindow{Seconds: 5, P99Ms: 900, Samples: 500}
	driveUntil(t, c, f, 30, func() bool { return len(f.holds) > 0 })
	f.override = nil
	drive(c, f, 90)

	want := []int{140, 155, 167, 176, 182, 187, 191}
	if len(f.holds) < len(want) || !slices.Equal(f.holds[:len(want)], want) {
		t.Fatalf("holds = %v, want prefix %v", f.holds, want)
	}
	if f.releases != 1 || f.hold != 0 {
		t.Fatalf("releases=%d hold=%d, want one release and no hold", f.releases, f.hold)
	}
	if _, state := c.Status(); state != StateWatching {
		t.Fatalf("state after release = %s", state)
	}
	if !slices.Contains(rec.states(), StateReleasing) {
		t.Fatalf("never reported releasing: %v", rec.states())
	}
}

func TestGuard_ReBreachDuringReleaseClampsAgainFromCurrentRate(t *testing.T) {
	f := newFake(t0, 200, 150)
	rec := &recorder{}
	c := mustNew(t, guardParams(), f, rec, staticSchedule(f))
	c.Start(t0)
	driveUntil(t, c, f, 120, func() bool { return len(f.holds) >= 3 && f.holds[2] < f.holds[1] })
	// 140 -> release step 155 (over the 150 knee) -> clamp from 155 to 108.
	if f.holds[0] != 140 || f.holds[1] != 155 || f.holds[2] != 108 {
		t.Fatalf("holds = %v, want 140, 155, 108", f.holds)
	}
}

func TestGuard_FlapLimitHoldsForGood(t *testing.T) {
	f := newFake(t0, 200, 150)
	rec := &recorder{}
	c := mustNew(t, guardParams(), f, rec, staticSchedule(f))
	c.Start(t0)
	drive(c, f, 400)

	clamps := 0
	for _, e := range rec.events {
		if e.State == StateClamped && strings.HasPrefix(e.Reason, "clamped to") {
			clamps++
		}
	}
	if clamps != maxClamps {
		t.Fatalf("clamp cycles = %d, want %d; holds=%v", clamps, maxClamps, f.holds)
	}
	if !rec.hasMark("SLO Guard: holding, target cannot sustain profile") {
		t.Fatalf("holding mark missing: %q", rec.marks)
	}
	releasesAtLimit := f.releases
	held := f.hold
	drive(c, f, 200)
	if f.releases != releasesAtLimit || f.hold != held || held == 0 {
		t.Fatalf("guard let go after the limit: hold %d -> %d, releases %d -> %d", held, f.hold, releasesAtLimit, f.releases)
	}
}

func TestGuard_ReleasesWhenScheduleDropsBelowClamp(t *testing.T) {
	f := newFake(t0, 200, 150)
	rec := &recorder{}
	c := mustNew(t, guardParams(), f, rec, func() (int, bool) { return f.schedule, true })
	c.Start(t0)
	driveUntil(t, c, f, 30, func() bool { return len(f.holds) > 0 })
	f.schedule = 100 // a ramp-down stage took the plan under the clamp
	drive(c, f, 3)
	if f.releases != 1 {
		t.Fatalf("releases = %d, want 1 (hold %d)", f.releases, f.hold)
	}
}

func TestGuard_ThinWindowNeverTrips(t *testing.T) {
	f := newFake(t0, 200, 150)
	f.override = &engine.HealthWindow{Seconds: 5, P99Ms: 900, ErrorRate: 0.5, Samples: 10}
	c := mustNew(t, guardParams(), f, &recorder{}, staticSchedule(f))
	c.Start(t0)
	drive(c, f, 60)
	if len(f.holds) != 0 {
		t.Fatalf("clamped on 10 samples: %v", f.holds)
	}
}

func TestGuard_ErrorRateAloneTrips(t *testing.T) {
	f := newFake(t0, 200, 1000)
	f.override = &engine.HealthWindow{Seconds: 5, P99Ms: 20, ErrorRate: 0.2, Samples: 500}
	rec := &recorder{}
	c := mustNew(t, guardParams(), f, rec, staticSchedule(f))
	c.Start(t0)
	driveUntil(t, c, f, 10, func() bool { return len(f.holds) > 0 })
	if len(f.holds) == 0 || !rec.hasMark("errors=20.0% > 1.0%") {
		t.Fatalf("holds=%v marks=%q", f.holds, rec.marks)
	}
}

func TestParams_DefaultsAndValidation(t *testing.T) {
	p := Params{Mode: ModeFindCeiling}.WithDefaults()
	if err := p.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	if p.StartRPS != 20 || p.StepPct != 25 || p.MinSamples != 50 || p.MaxP99Ms != 250 || p.ConfirmHold != 15*time.Second {
		t.Fatalf("unexpected defaults: %+v", p)
	}
	bad := []Params{
		{Mode: "bogus"},
		{Mode: ModeFindCeiling, MaxErrorRate: 2},
		{Mode: ModeFindCeiling, StartRPS: 50, MaxRPS: 10},
		{Mode: ModeFindCeiling, StepRPS: -1},
		{Mode: ModeFindCeiling, StepInterval: time.Millisecond},
	}
	for _, b := range bad {
		if err := b.WithDefaults().Validate(); err == nil {
			t.Errorf("%+v validated", b)
		}
	}
}

func TestScheduleAt(t *testing.T) {
	stages := []struct {
		d time.Duration
		r int
	}{{10 * time.Second, 100}, {0, 400}, {10 * time.Second, 400}, {10 * time.Second, 0}}
	cfg := make([]config.Stage, len(stages))
	for i, s := range stages {
		cfg[i] = config.Stage{Duration: s.d, TargetRPS: s.r}
	}
	tests := []struct {
		at    time.Duration
		scale float64
		want  int
	}{
		{0, 1, 0},
		{5 * time.Second, 1, 50},
		{10 * time.Second, 1, 400}, // instant step lands at the boundary
		{15 * time.Second, 1, 400},
		{25 * time.Second, 1, 200},
		{time.Hour, 1, 0},
		{2500 * time.Millisecond, 2, 50}, // twice as fast: the 10s stage takes 5s
		{5 * time.Second, 2, 400},
	}
	for _, tc := range tests {
		if got := ScheduleAt(cfg, tc.scale, tc.at); got != tc.want {
			t.Errorf("ScheduleAt(%v, x%v) = %d, want %d", tc.at, tc.scale, got, tc.want)
		}
	}
}
