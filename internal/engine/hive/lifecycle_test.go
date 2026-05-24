package hive

import (
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// setTimes directly sets startTime and endTime under the timeMu lock,
// simulating what RunStages would do without needing a live HTTP server.
func setTimes(e *Engine, start, end time.Time) {
	e.timeMu.Lock()
	e.startTime = start
	e.endTime = end
	e.timeMu.Unlock()
}

// ── IsRunning ─────────────────────────────────────────────────────────────────

func TestIsRunning_FalseBeforeStart(t *testing.T) {
	e := New()
	if e.IsRunning() {
		t.Fatal("expected IsRunning=false before any start")
	}
}

func TestIsRunning_TrueDuringRun(t *testing.T) {
	e := New()
	e.isRunning.Store(true)
	if !e.IsRunning() {
		t.Fatal("expected IsRunning=true while running")
	}
}

func TestIsRunning_FalseAfterEnd(t *testing.T) {
	e := New()
	e.isRunning.Store(true)
	e.isRunning.Store(false)
	if e.IsRunning() {
		t.Fatal("expected IsRunning=false after run ends")
	}
}

// ── GetStartTime ──────────────────────────────────────────────────────────────

func TestGetStartTime_ZeroBeforeStart(t *testing.T) {
	e := New()
	if !e.GetStartTime().IsZero() {
		t.Fatal("expected zero start time before any run")
	}
}

func TestGetStartTime_SetAfterStart(t *testing.T) {
	e := New()
	now := time.Now()
	setTimes(e, now, time.Time{})
	got := e.GetStartTime()
	if !got.Equal(now) {
		t.Fatalf("expected start=%v, got %v", now, got)
	}
}

func TestGetStartTime_StableAfterEnd(t *testing.T) {
	e := New()
	start := time.Now()
	end := start.Add(2 * time.Second)
	setTimes(e, start, end)
	if !e.GetStartTime().Equal(start) {
		t.Fatal("start time must not change after run ends")
	}
}

// ── GetEndTime ────────────────────────────────────────────────────────────────

func TestGetEndTime_ReturnsNowBeforeStart(t *testing.T) {
	e := New()
	before := time.Now()
	got := e.GetEndTime()
	after := time.Now()
	// GetEndTime falls back to time.Now() when endTime is zero.
	if got.Before(before) || got.After(after) {
		t.Fatalf("expected GetEndTime≈now, got %v (window %v–%v)", got, before, after)
	}
}

func TestGetEndTime_ReturnsNowDuringRun(t *testing.T) {
	e := New()
	start := time.Now().Add(-500 * time.Millisecond)
	setTimes(e, start, time.Time{}) // endTime still zero → still running
	e.isRunning.Store(true)
	before := time.Now()
	got := e.GetEndTime()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("expected GetEndTime≈now during run, got %v", got)
	}
}

func TestGetEndTime_ReturnsRecordedTimeAfterEnd(t *testing.T) {
	e := New()
	start := time.Now().Add(-2 * time.Second)
	end := time.Now()
	setTimes(e, start, end)
	got := e.GetEndTime()
	if !got.Equal(end) {
		t.Fatalf("expected end=%v, got %v", end, got)
	}
}

// ── GetElapsedTime ────────────────────────────────────────────────────────────

func TestGetElapsedTime_ZeroBeforeStart(t *testing.T) {
	e := New()
	if e.GetElapsedTime() != 0 {
		t.Fatalf("expected 0 before start, got %v", e.GetElapsedTime())
	}
}

func TestGetElapsedTime_LiveDuringRun(t *testing.T) {
	e := New()
	start := time.Now().Add(-500 * time.Millisecond)
	setTimes(e, start, time.Time{}) // endTime zero = still running
	e.isRunning.Store(true)
	elapsed := e.GetElapsedTime()
	// Should be ~ 0.5 s, allow 100 ms slack for CI jitter.
	if elapsed < 0.4 || elapsed > 2.0 {
		t.Fatalf("expected elapsed≈0.5s during run, got %.3fs", elapsed)
	}
}

func TestGetElapsedTime_FixedAfterEnd(t *testing.T) {
	e := New()
	start := time.Now().Add(-3 * time.Second)
	end := start.Add(3 * time.Second)
	setTimes(e, start, end)
	elapsed := e.GetElapsedTime()
	// Must be exactly 3.0 s (sub-millisecond precision is fine).
	if elapsed < 2.999 || elapsed > 3.001 {
		t.Fatalf("expected elapsed=3.0s after end, got %.6fs", elapsed)
	}
}

func TestGetElapsedTime_DoesNotGrowAfterEnd(t *testing.T) {
	e := New()
	start := time.Now().Add(-1 * time.Second)
	end := start.Add(time.Second)
	setTimes(e, start, end)
	first := e.GetElapsedTime()
	time.Sleep(20 * time.Millisecond)
	second := e.GetElapsedTime()
	if first != second {
		t.Fatalf("elapsed time must be fixed after end: first=%.6f second=%.6f", first, second)
	}
}

// ── Combined state machine ────────────────────────────────────────────────────

func TestLifecycle_FullStateMachine(t *testing.T) {
	e := New()

	// 1. Before start
	if e.IsRunning() {
		t.Fatal("state 1: expected IsRunning=false")
	}
	if !e.GetStartTime().IsZero() {
		t.Fatal("state 1: expected zero start time")
	}
	if e.GetElapsedTime() != 0 {
		t.Fatal("state 1: expected elapsed=0")
	}

	// 2. Simulate run start
	start := time.Now()
	setTimes(e, start, time.Time{})
	e.isRunning.Store(true)
	if !e.IsRunning() {
		t.Fatal("state 2: expected IsRunning=true")
	}
	if e.GetStartTime().IsZero() {
		t.Fatal("state 2: expected non-zero start time")
	}

	// 3. Simulate run end (10 ms later)
	time.Sleep(10 * time.Millisecond)
	end := time.Now()
	setTimes(e, start, end)
	e.isRunning.Store(false)

	if e.IsRunning() {
		t.Fatal("state 3: expected IsRunning=false")
	}
	got := e.GetElapsedTime()
	expected := end.Sub(start).Seconds()
	if got != expected {
		t.Fatalf("state 3: expected elapsed=%.6f, got %.6f", expected, got)
	}
	if !e.GetEndTime().Equal(end) {
		t.Fatalf("state 3: expected end=%v, got %v", end, e.GetEndTime())
	}
}
