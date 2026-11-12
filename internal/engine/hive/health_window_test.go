package hive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// newHealthEngine is an engine with a latency ring big enough to hold a whole test run.
func newHealthEngine() *Engine {
	e := New()
	b := newLatencyBuf(16 * 1024)
	e.latBuf.Store(&b)
	return e
}

// feed records n completed requests at one latency, failed of them failing.
func feed(e *Engine, n, failed int, d time.Duration) {
	for i := 0; i < n; i++ {
		e.counters.incTotalRequests(0)
		if i < failed {
			e.counters.incFailure(0)
		} else {
			e.counters.incSuccess(0)
		}
		e.recordLatency(0, d)
	}
}

// A target that degrades at t=10s and recovers at t=20s: the 5 s window is back
// under threshold within one window length, the cumulative figures are not.
func TestHealthSince_RecoversWithinOneWindow(t *testing.T) {
	e := newHealthEngine()
	e.sampleHealth(t0)

	var windowAt = map[int]struct{ rate, p99 float64 }{}
	for s := 1; s <= 40; s++ {
		if s > 10 && s <= 20 {
			feed(e, 100, 50, 500*time.Millisecond)
		} else {
			feed(e, 100, 0, 10*time.Millisecond)
		}
		now := t0.Add(time.Duration(s) * time.Second)
		e.sampleHealth(now)
		w, ok := e.healthSince(now, 5*time.Second)
		if !ok {
			t.Fatalf("no window at t=%d", s)
		}
		windowAt[s] = struct{ rate, p99 float64 }{w.ErrorRate, w.P99Ms}
	}

	if w := windowAt[15]; w.rate < 0.49 || w.p99 < 400 {
		t.Errorf("t=15 inside the outage: %+v", w)
	}
	if w := windowAt[25]; w.rate != 0 || w.p99 > 11 {
		t.Errorf("t=25, one window after recovery: %+v", w)
	}

	m := e.GetMetrics()
	if m.ErrorRate < 0.1 {
		t.Errorf("cumulative error rate should still show the outage, got %v", m.ErrorRate)
	}
	if m.P99Latency < 400 {
		t.Errorf("cumulative p99 should still be polluted, got %v", m.P99Latency)
	}
}

func TestHealthSince_ErrorRateUsesCompletedRequests(t *testing.T) {
	e := New()
	e.sampleHealth(t0)
	feed(e, 50, 10, time.Millisecond)
	// 30 requests started but not finished must not dilute the rate.
	for i := 0; i < 30; i++ {
		e.counters.incTotalRequests(0)
	}
	now := t0.Add(5 * time.Second)
	w, ok := e.healthSince(now, 5*time.Second)
	if !ok || w.ErrorRate != 0.2 {
		t.Errorf("window = %+v ok=%v, want error rate 0.2", w, ok)
	}
}

func TestHealthSince_EdgeCases(t *testing.T) {
	t.Run("no readings yet", func(t *testing.T) {
		if _, ok := New().healthSince(t0, 5*time.Second); ok {
			t.Error("want ok=false before any reading")
		}
	})

	t.Run("idle window", func(t *testing.T) {
		e := New()
		e.sampleHealth(t0)
		w, ok := e.healthSince(t0.Add(5*time.Second), 5*time.Second)
		if !ok || w.ErrorRate != 0 || w.Samples != 0 || w.P99Ms != 0 {
			t.Errorf("idle window = %+v ok=%v", w, ok)
		}
	})

	t.Run("run younger than the window uses what exists", func(t *testing.T) {
		e := New()
		e.sampleHealth(t0)
		feed(e, 20, 0, 5*time.Millisecond)
		w, ok := e.healthSince(t0.Add(2*time.Second), 5*time.Second)
		if !ok || w.Seconds != 2 || w.Samples != 20 {
			t.Errorf("window = %+v ok=%v, want 2 s / 20 samples", w, ok)
		}
	})

	t.Run("window longer than the ring clamps to the oldest kept reading", func(t *testing.T) {
		e := New()
		for s := 0; s < healthRingSize+20; s++ {
			feed(e, 1, 0, time.Millisecond)
			e.sampleHealth(t0.Add(time.Duration(s) * time.Second))
		}
		now := t0.Add(time.Duration(healthRingSize+19) * time.Second)
		w, ok := e.healthSince(now, time.Hour)
		if !ok || w.Seconds != float64(healthRingSize-1) {
			t.Errorf("window = %+v ok=%v, want %d s", w, ok, healthRingSize-1)
		}
	})

	t.Run("restarted run invalidates old readings", func(t *testing.T) {
		e := New()
		feed(e, 50, 0, time.Millisecond)
		e.sampleHealth(t0)
		b := newLatencyBuf(1024)
		e.latBuf.Store(&b) // what RunStages does on a new run
		if _, ok := e.healthSince(t0.Add(time.Second), time.Second); ok {
			t.Error("want ok=false when the counters went backwards")
		}
	})

	t.Run("more samples than one read: capped", func(t *testing.T) {
		e := newHealthEngine()
		e.sampleHealth(t0)
		feed(e, latencySampleSize+500, 0, time.Millisecond)
		w, ok := e.healthSince(t0.Add(5*time.Second), 5*time.Second)
		if !ok || !w.Capped || w.Samples != latencySampleSize {
			t.Errorf("window = %+v ok=%v, want capped at %d", w, ok, latencySampleSize)
		}
	})

	t.Run("p99 sees only samples inside the window", func(t *testing.T) {
		e := New()
		e.sampleHealth(t0)
		feed(e, 200, 0, 900*time.Millisecond) // old and slow
		e.sampleHealth(t0.Add(time.Second))
		feed(e, 200, 0, 5*time.Millisecond)
		now := t0.Add(2 * time.Second)
		e.sampleHealth(now)
		w, _ := e.healthSince(now, time.Second)
		if w.Samples != 200 || w.P99Ms > 6 {
			t.Errorf("window = %+v, want only the recent fast samples", w)
		}
	})
}

func TestHealthSampler_StopsAndRestartsClean(t *testing.T) {
	e := New()
	stop := e.startHealthSampler()
	if _, ok := e.HealthSince(time.Second); !ok {
		t.Error("a reading is taken immediately at start")
	}
	stop()
	stop2 := e.startHealthSampler()
	stop2()
	e.health.mu.Lock()
	n := e.health.n
	e.health.mu.Unlock()
	if n != 1 {
		t.Errorf("a restart should clear old readings, have %d", n)
	}
}

// Through a real run: the windowed figures follow the target's behaviour.
func TestRunStages_HealthWindowTracksTarget(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)

	e := New()
	done := make(chan error, 1)
	go func() {
		done <- e.RunStages(context.Background(), singleStageCfg(5*time.Second, 40), specsFor(srv.URL))
	}()

	time.Sleep(1500 * time.Millisecond)
	if w, ok := e.HealthSince(time.Second); !ok || w.ErrorRate != 0 || w.Samples == 0 {
		t.Errorf("healthy phase window = %+v ok=%v", w, ok)
	}
	fail.Store(true)
	time.Sleep(2500 * time.Millisecond)
	if w, ok := e.HealthSince(2 * time.Second); !ok || w.ErrorRate < 0.9 {
		t.Errorf("failing phase window = %+v ok=%v, want ~all failing", w, ok)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
