package autopilot

import (
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/engine"
)

// segment is a span of the run served at one rate.
type segment struct {
	from time.Time
	rate int
}

// fakeTarget simulates a service: health depends on the rate being served. A
// window's figures come from every segment it overlaps, as with the real engine.
type fakeTarget struct {
	now      time.Time
	hold     int
	schedule int        // rate served when no hold is active
	knee     func() int // rates above this breach; evaluated each query
	perSec   int        // latency samples per second of traffic
	segs     []segment  // oldest first
	holds    []int      // every HoldRPS call, in order
	releases int        // ReleaseHold calls
	top      []engine.EndpointError
	override *engine.HealthWindow // returned verbatim when set, bypassing the model
}

func newFake(start time.Time, schedule, knee int) *fakeTarget {
	f := &fakeTarget{now: start, schedule: schedule, perSec: 100, knee: func() int { return knee }}
	f.segs = []segment{{from: start, rate: schedule}}
	return f
}

func (f *fakeTarget) rate() int {
	if f.hold > 0 {
		return f.hold
	}
	return f.schedule
}

func (f *fakeTarget) mark() { f.segs = append(f.segs, segment{from: f.now, rate: f.rate()}) }

func (f *fakeTarget) HoldRPS(rps int) { f.hold = rps; f.holds = append(f.holds, rps); f.mark() }
func (f *fakeTarget) ReleaseHold()    { f.hold = 0; f.releases++; f.mark() }
func (f *fakeTarget) GetHoldRPS() int { return f.hold }

func (f *fakeTarget) GetMetrics() *engine.MetricsSnapshot {
	return &engine.MetricsSnapshot{Throughput: float64(f.rate()), TargetRPS: f.rate()}
}

func (f *fakeTarget) HealthSince(d time.Duration) (engine.HealthWindow, bool) {
	if f.override != nil {
		return *f.override, true
	}
	from := f.now.Add(-d)
	w := engine.HealthWindow{Seconds: d.Seconds()}
	for i, s := range f.segs {
		end := f.now
		if i+1 < len(f.segs) {
			end = f.segs[i+1].from
		}
		if !end.After(from) {
			continue
		}
		if s.rate > f.knee() {
			w.P99Ms = 400
			w.ErrorRate = 0.05
		} else if w.P99Ms < 50 {
			w.P99Ms = 50
		}
	}
	w.Samples = int(d.Seconds() * float64(f.perSec))
	return w, true
}

func (f *fakeTarget) RecentErrorMix(time.Duration) (engine.ErrorMix, bool) {
	if len(f.top) == 0 {
		return engine.ErrorMix{}, false
	}
	return engine.ErrorMix{Sampled: 10, TopEndpoints: f.top}, true
}

// recorder is a Sink that keeps everything the controller says.
type recorder struct {
	marks   []string
	events  []Event
	stopped int
}

func (r *recorder) Mark(l string) { r.marks = append(r.marks, l) }
func (r *recorder) Event(e Event) { r.events = append(r.events, e) }
func (r *recorder) StopRun()      { r.stopped++ }

func (r *recorder) states() []State {
	out := make([]State, len(r.events))
	for i, e := range r.events {
		out[i] = e.State
	}
	return out
}

func (r *recorder) hasMark(sub string) bool {
	for _, m := range r.marks {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// drive ticks the controller once a second for up to seconds, stopping early
// when it is done. It returns the seconds used.
func drive(c *Controller, f *fakeTarget, seconds int) int {
	for i := 1; i <= seconds; i++ {
		f.now = f.now.Add(time.Second)
		c.Tick(f.now)
		select {
		case <-c.Done():
			return i
		default:
		}
	}
	return seconds
}

// driveUntil ticks until pred is true and reports whether it became true.
func driveUntil(t *testing.T, c *Controller, f *fakeTarget, seconds int, pred func() bool) bool {
	t.Helper()
	for i := 0; i < seconds; i++ {
		if pred() {
			return true
		}
		drive(c, f, 1)
	}
	return pred()
}

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func mustNew(t *testing.T, p Params, f *fakeTarget, rec *recorder, sched func() (int, bool)) *Controller {
	t.Helper()
	c, err := New(p, f, rec, sched)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}
