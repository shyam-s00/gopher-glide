package autopilot

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/engine"
)

// maxConfirmAttempts bounds how often find-ceiling steps down and re-confirms.
const maxConfirmAttempts = 3

// maxClamps is how many clamp cycles slo-guard allows before it stops releasing.
const maxClamps = 3

// guardWindow is the trailing span slo-guard watches while idle.
const guardWindow = 5 * time.Second

// clampFactor is the share of the current actual rate a clamp holds at.
const clampFactor = 0.7

// recoverMargin is the share of each threshold health must be under to recover.
const recoverMargin = 0.8

// Target is the slice of the engine the controller drives.
type Target interface {
	engine.Holder
	engine.WindowedHealth
	GetMetrics() *engine.MetricsSnapshot
}

// Sink receives what the controller decides. Calls come from the controller's
// goroutine and must not block for long.
type Sink interface {
	// Mark records an annotation on the run timeline.
	Mark(label string)
	// Event reports a state change.
	Event(e Event)
	// StopRun ends the run cleanly; the controller calls it once, when done.
	StopRun()
}

// Event is one controller state change.
type Event struct {
	Mode       Mode    `json:"mode"`
	State      State   `json:"state"`
	HoldRPS    int     `json:"hold_rps,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	CeilingRPS int     `json:"ceiling_rps,omitempty"`
	Outcome    Outcome `json:"outcome,omitempty"`
	ElapsedS   float64 `json:"elapsed_s"`
}

// Result is what a finished controller reports.
type Result struct {
	Mode       Mode
	Outcome    Outcome
	CeilingRPS int
	Params     Params
	Events     []Event
	// Closing is the final event when Finalize had to produce it (the controller
	// was cut off before deciding); nil when the controller already emitted it.
	Closing *Event
}

// Controller is the closed loop. Drive it with Start and then Tick once a
// second (Run does that); it is not safe for concurrent Tick calls.
type Controller struct {
	p        Params
	t        Target
	sink     Sink
	schedule func() (int, bool)

	mu     sync.Mutex // guards last, which Status reads from other goroutines
	last   Event
	events []Event

	start, changeAt time.Time
	state           State
	hold            int
	outcome         Outcome
	ceiling         int
	done            chan struct{}

	// find-ceiling
	highest int // highest rate judged healthy
	attempt int // confirmation attempt, 1-based

	// slo-guard
	over         int
	clamps       int
	recoverSince time.Time
	holding      bool // anti-flap engaged: never release again
}

// New builds a controller. schedule reports the stage plan's current rate for
// slo-guard's stepped release; it may be nil for find-ceiling.
func New(p Params, t Target, sink Sink, schedule func() (int, bool)) (*Controller, error) {
	p = p.WithDefaults()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if schedule == nil {
		schedule = func() (int, bool) { return 0, false }
	}
	return &Controller{p: p, t: t, sink: sink, schedule: schedule, done: make(chan struct{})}, nil
}

// Params returns the effective, defaulted parameters.
func (c *Controller) Params() Params { return c.p }

// Done is closed once the controller has reached an outcome.
func (c *Controller) Done() <-chan struct{} { return c.done }

// Status is the controller's current mode and state, for heartbeats.
func (c *Controller) Status() (Mode, State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.p.Mode, c.last.State
}

// Start takes the controller's first action: find-ceiling pins the start rate,
// slo-guard begins watching. Call it before the engine starts, so the first
// window already runs at the held rate.
func (c *Controller) Start(now time.Time) {
	c.start, c.changeAt = now, now
	if c.p.Mode == ModeFindCeiling {
		c.attempt = 1
		c.setHold(now, minInt(c.p.StartRPS, c.p.MaxRPS), StateStepping,
			fmt.Sprintf("starting at %d RPS", minInt(c.p.StartRPS, c.p.MaxRPS)))
		return
	}
	c.state = StateWatching
	c.emit(now, StateWatching, fmt.Sprintf("watching p99 <= %.0fms, errors <= %.1f%%", c.p.MaxP99Ms, c.p.MaxErrorRate*100))
}

// Run ticks once a second until ctx is cancelled or the controller is done.
func (c *Controller) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case now := <-t.C:
			c.Tick(now)
		}
	}
}

// Tick runs one control step.
func (c *Controller) Tick(now time.Time) {
	if c.outcome != "" {
		return
	}
	if c.p.Mode == ModeFindCeiling {
		c.tickFind(now)
	} else {
		c.tickGuard(now)
	}
}

// Finalize closes the controller out after the run is over. runEnded is true
// when the run finished on its own rather than being stopped.
func (c *Controller) Finalize(runEnded bool) Result {
	var closing *Event
	if c.outcome == "" {
		switch {
		case c.p.Mode == ModeSLOGuard && runEnded:
			c.outcome = OutcomeCompleted
		case c.p.Mode == ModeFindCeiling && runEnded && c.state == StateStepping && c.highest > 0:
			c.outcome, c.ceiling = OutcomeCeilingNotReached, c.highest
		default:
			c.outcome = OutcomeStopped
		}
		c.state = StateDone
		e := c.newEvent(time.Now(), StateDone, "run ended")
		c.keep(e)
		closing = &e
	}
	return Result{Mode: c.p.Mode, Outcome: c.outcome, CeilingRPS: c.ceiling, Params: c.p,
		Events: append([]Event(nil), c.events...), Closing: closing}
}

// healthRead is one windowed reading and what to make of it.
type healthRead struct {
	w       engine.HealthWindow
	ready   bool // enough samples (or the wait was given up) to judge
	breach  bool
	reason  string
	healthy bool // under the recovery margin on both metrics
}

// read judges health since from, over at most maxSpan. After a change it waits
// for MinSamples, but gives up after extendCap and judges what it has.
func (c *Controller) read(now, from time.Time, maxSpan time.Duration, extend bool) healthRead {
	d := now.Sub(from)
	if d < time.Second {
		return healthRead{}
	}
	if maxSpan > 0 && d > maxSpan {
		d = maxSpan
	}
	w, ok := c.t.HealthSince(d)
	if !ok {
		return healthRead{}
	}
	r := healthRead{w: w, ready: w.Samples >= c.p.MinSamples}
	if !r.ready {
		if !extend || now.Sub(c.changeAt) < c.extendCap() {
			return r
		}
		r.ready = true
		if w.Samples == 0 {
			r.breach, r.reason = true, "no completed requests in the window"
			return r
		}
	}
	var why []string
	if w.P99Ms > c.p.MaxP99Ms {
		why = append(why, fmt.Sprintf("p99=%.0fms > %.0fms", w.P99Ms, c.p.MaxP99Ms))
	}
	if w.ErrorRate > c.p.MaxErrorRate {
		why = append(why, fmt.Sprintf("errors=%.1f%% > %.1f%%", w.ErrorRate*100, c.p.MaxErrorRate*100))
	}
	if len(why) > 0 {
		r.breach, r.reason = true, strings.Join(why, ", ")
	}
	r.healthy = !r.breach && w.P99Ms <= c.p.MaxP99Ms*recoverMargin && w.ErrorRate <= c.p.MaxErrorRate*recoverMargin
	return r
}

func (c *Controller) extendCap() time.Duration { return 6*c.p.StepInterval + c.p.Settle }

func (c *Controller) settled() time.Time { return c.changeAt.Add(c.p.Settle) }

func (c *Controller) tickFind(now time.Time) {
	if now.Sub(c.start) >= c.p.MaxDuration {
		if c.state == StateStepping && c.highest > 0 {
			c.finish(now, OutcomeCeilingNotReached, c.highest, fmt.Sprintf("max duration reached; highest healthy rate %d RPS", c.highest))
		} else {
			c.finish(now, OutcomeUnstable, 0, "max duration reached before a rate could be confirmed")
		}
		return
	}
	r := c.read(now, c.settled(), 0, true)
	if !r.ready {
		return
	}
	if c.state == StateConfirming {
		c.tickConfirm(now, r)
		return
	}
	if r.breach {
		c.onBreach(now, r)
		return
	}
	if now.Sub(c.changeAt) < c.p.StepInterval {
		return
	}
	c.highest = c.hold
	if c.hold >= c.p.MaxRPS {
		c.finish(now, OutcomeCeilingNotReached, c.highest, fmt.Sprintf("healthy up to the %d RPS cap", c.p.MaxRPS))
		return
	}
	next := minInt(c.stepUp(c.hold), c.p.MaxRPS)
	c.setHold(now, next, StateStepping, fmt.Sprintf("healthy at %d RPS (p99=%.0fms, errors=%.2f%%); stepping to %d",
		c.hold, r.w.P99Ms, r.w.ErrorRate*100, next))
}

func (c *Controller) onBreach(now time.Time, r healthRead) {
	back := c.stepDown(c.hold)
	if c.highest > 0 && c.highest < c.hold {
		back = c.highest
	}
	why := fmt.Sprintf("Autopilot: ceiling breached at %d RPS (%s%s); backing off to %d RPS", c.hold, r.reason, c.evidence(), back)
	c.sink.Mark(why)
	c.emit(now, StateBreached, why)
	if back >= c.hold {
		c.finish(now, OutcomeUnstable, 0, fmt.Sprintf("breached at %d RPS with nowhere lower to go", c.hold))
		return
	}
	c.attempt = 1
	c.setHold(now, back, StateConfirming, fmt.Sprintf("confirming %d RPS for %s", back, c.p.ConfirmHold))
}

func (c *Controller) tickConfirm(now time.Time, r healthRead) {
	if r.breach {
		if c.attempt >= maxConfirmAttempts {
			c.finish(now, OutcomeUnstable, 0, fmt.Sprintf("still unhealthy at %d RPS after %d confirmations (%s)", c.hold, c.attempt, r.reason))
			return
		}
		down := c.stepDown(c.hold)
		if down >= c.hold {
			c.finish(now, OutcomeUnstable, 0, fmt.Sprintf("still unhealthy at %d RPS with nowhere lower to go", c.hold))
			return
		}
		c.sink.Mark(fmt.Sprintf("Autopilot: still unhealthy at %d RPS (%s%s); stepping down to %d RPS", c.hold, r.reason, c.evidence(), down))
		c.attempt++
		c.setHold(now, down, StateConfirming, fmt.Sprintf("confirming %d RPS for %s (attempt %d)", down, c.p.ConfirmHold, c.attempt))
		return
	}
	if now.Sub(c.changeAt) >= c.p.ConfirmHold {
		c.finish(now, OutcomeCeilingFound, c.hold, fmt.Sprintf("ceiling confirmed at %d RPS", c.hold))
	}
}

func (c *Controller) tickGuard(now time.Time) {
	var r healthRead
	if c.state == StateWatching {
		r = c.read(now, now.Add(-guardWindow), guardWindow, false)
	} else {
		r = c.read(now, c.settled(), guardWindow, true)
	}

	if c.state != StateWatching {
		if sched, ok := c.schedule(); ok && sched <= c.hold {
			c.releaseGuard(now, "schedule is at or below the clamp")
			return
		}
	}

	switch {
	case !r.ready:
		c.over = 0
	case r.breach:
		c.over++
	default:
		c.over = 0
	}
	if c.over >= 2 {
		c.clamp(now, r)
		return
	}

	switch c.state {
	case StateClamped:
		c.tickClamped(now, r)
	case StateReleasing:
		if r.ready && now.Sub(c.changeAt) >= c.p.StepInterval {
			c.releaseStep(now)
		}
	}
}

func (c *Controller) clamp(now time.Time, r healthRead) {
	rate := int(c.t.GetMetrics().Throughput)
	if rate < 1 {
		rate = c.hold
	}
	to := maxInt(1, int(float64(rate)*clampFactor))
	c.clamps++
	c.over, c.recoverSince = 0, time.Time{}
	c.sink.Mark(fmt.Sprintf("Autopilot SLO Guard triggered: %s%s; clamping %d to %d RPS", r.reason, c.evidence(), rate, to))
	c.setHold(now, to, StateClamped, fmt.Sprintf("clamped to %d RPS (%s)", to, r.reason))
}

func (c *Controller) tickClamped(now time.Time, r healthRead) {
	if c.holding || !r.ready {
		return
	}
	if !r.healthy {
		c.recoverSince = time.Time{}
		return
	}
	if c.recoverSince.IsZero() {
		c.recoverSince = now
	}
	if now.Sub(c.recoverSince) < c.p.RecoverHold {
		return
	}
	if c.clamps >= maxClamps {
		c.holding = true
		c.sink.Mark("SLO Guard: holding, target cannot sustain profile")
		c.emit(now, StateClamped, fmt.Sprintf("holding %d RPS to the end of the run; the target cannot sustain the profile", c.hold))
		return
	}
	c.state = StateReleasing
	c.releaseStep(now)
}

// releaseStep raises the hold a quarter of the way to the schedule, or lets go
// once it is within 5% of it.
func (c *Controller) releaseStep(now time.Time) {
	target, ok := c.schedule()
	if !ok || target <= c.hold || float64(c.hold) >= 0.95*float64(target) {
		c.releaseGuard(now, "back on the schedule")
		return
	}
	next := minInt(target, c.hold+maxInt(1, int(math.Ceil(0.25*float64(target-c.hold)))))
	c.setHold(now, next, StateReleasing, fmt.Sprintf("releasing toward %d RPS: holding %d", target, next))
}

func (c *Controller) releaseGuard(now time.Time, reason string) {
	c.t.ReleaseHold()
	c.hold, c.over, c.recoverSince = 0, 0, time.Time{}
	c.changeAt = now
	c.state = StateWatching
	c.emit(now, StateWatching, "hold released: "+reason)
}

// finish records the outcome, marks it, tells the host to end the run and
// closes Done. It is the only way the controller reaches done on its own.
func (c *Controller) finish(now time.Time, o Outcome, ceiling int, reason string) {
	c.outcome, c.ceiling = o, ceiling
	c.state = StateDone
	switch o {
	case OutcomeCeilingFound:
		c.sink.Mark(fmt.Sprintf("Autopilot: ceiling confirmed at %d RPS", ceiling))
	case OutcomeCeilingNotReached:
		c.sink.Mark("Autopilot: no ceiling found — " + reason)
	case OutcomeUnstable:
		c.sink.Mark("Autopilot: unstable — " + reason)
	}
	c.send(c.newEvent(now, StateDone, reason))
	c.sink.StopRun()
	close(c.done)
}

func (c *Controller) setHold(now time.Time, rps int, st State, reason string) {
	c.t.HoldRPS(rps)
	c.hold, c.changeAt = rps, now
	c.state = st
	c.emit(now, st, reason)
}

func (c *Controller) emit(now time.Time, st State, reason string) {
	c.send(c.newEvent(now, st, reason))
}

func (c *Controller) newEvent(now time.Time, st State, reason string) Event {
	e := Event{Mode: c.p.Mode, State: st, HoldRPS: c.hold, Reason: reason, ElapsedS: elapsed(c.start, now)}
	if st == StateDone {
		e.Outcome, e.CeilingRPS = c.outcome, c.ceiling
	}
	return e
}

// keep appends e to the history and makes it the current status.
func (c *Controller) keep(e Event) {
	c.events = append(c.events, e)
	c.mu.Lock()
	c.last = e
	c.mu.Unlock()
}

func (c *Controller) send(e Event) {
	c.keep(e)
	c.sink.Event(e)
}

// evidence names the most-failing endpoint, when the engine can say.
func (c *Controller) evidence() string {
	em, ok := c.t.(engine.ErrorMixer)
	if !ok {
		return ""
	}
	mix, any := em.RecentErrorMix(10 * time.Second)
	if !any || len(mix.TopEndpoints) == 0 {
		return ""
	}
	top := mix.TopEndpoints[0]
	if top.Code > 0 {
		return fmt.Sprintf("; top failing: %s %d", top.Endpoint, top.Code)
	}
	return fmt.Sprintf("; top failing: %s %s", top.Endpoint, top.Kind)
}

func (c *Controller) stepUp(cur int) int {
	if c.p.StepRPS > 0 {
		return cur + c.p.StepRPS
	}
	return cur + maxInt(1, int(math.Ceil(float64(cur)*float64(c.p.StepPct)/100)))
}

func (c *Controller) stepDown(cur int) int {
	var down int
	if c.p.StepRPS > 0 {
		down = cur - c.p.StepRPS
	} else {
		down = int(float64(cur) * 100 / float64(100+c.p.StepPct))
	}
	if down >= cur {
		down = cur - 1
	}
	return maxInt(1, down)
}

func elapsed(start, now time.Time) float64 {
	if start.IsZero() || now.Before(start) {
		return 0
	}
	return math.Round(now.Sub(start).Seconds()*10) / 10
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
