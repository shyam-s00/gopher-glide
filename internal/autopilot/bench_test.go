package autopilot

import (
	"testing"
	"time"
)

// benchRun drives a fresh controller for ticks simulated seconds and reports the cost of one tick.
func benchRun(b *testing.B, p Params, knee, schedule int, withSchedule bool, ticks int) {
	b.Helper()
	b.ReportAllocs()
	var done int
	for i := 0; i < b.N; i++ {
		f := newFake(t0, schedule, knee)
		var sched func() (int, bool)
		if withSchedule {
			sched = staticSchedule(f)
		}
		c, err := New(p, f, &recorder{}, sched)
		if err != nil {
			b.Fatal(err)
		}
		c.Start(t0)
		f.mark()
		done += drive(c, f, ticks)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(done), "ns/tick")
}

// A whole find-ceiling search against a service with a knee: steps, breach, back-off, confirm.
func BenchmarkController_FindCeiling(b *testing.B) {
	p := findParams()
	p.MaxDuration = time.Hour
	benchRun(b, p, 100, 20, false, 300)
}

// A guard that clamps on a spike, releases in steps and keeps watching.
func BenchmarkController_SLOGuard(b *testing.B) {
	benchRun(b, guardParams(), 150, 200, true, 300)
}
