package hive

import (
	"testing"
	"time"
)

// ── metrics ───────────────────────────────────────────────────────────────────

func TestMetrics_ZeroState(t *testing.T) {
	var m metrics
	if v := m.totalRequests.Load(); v != 0 {
		t.Errorf("totalRequests: want 0, got %d", v)
	}
	if v := m.successCount.Load(); v != 0 {
		t.Errorf("successCount: want 0, got %d", v)
	}
	if v := m.failureCount.Load(); v != 0 {
		t.Errorf("failureCount: want 0, got %d", v)
	}
	if v := m.totalLatency.Load(); v != 0 {
		t.Errorf("totalLatency: want 0, got %d", v)
	}
}

func TestMetrics_AtomicAdd(t *testing.T) {
	var m metrics
	m.totalRequests.Add(10)
	m.successCount.Add(7)
	m.failureCount.Add(3)
	m.totalLatency.Add(500)

	if v := m.totalRequests.Load(); v != 10 {
		t.Errorf("totalRequests: want 10, got %d", v)
	}
	if v := m.successCount.Load(); v != 7 {
		t.Errorf("successCount: want 7, got %d", v)
	}
	if v := m.failureCount.Load(); v != 3 {
		t.Errorf("failureCount: want 3, got %d", v)
	}
	if v := m.totalLatency.Load(); v != 500 {
		t.Errorf("totalLatency: want 500, got %d", v)
	}
}

func TestMetrics_CounterInvariant(t *testing.T) {
	// success + failure must always equal totalRequests.
	var m metrics
	for i := 0; i < 20; i++ {
		m.totalRequests.Add(1)
		if i%3 == 0 {
			m.failureCount.Add(1)
		} else {
			m.successCount.Add(1)
		}
	}
	total := m.totalRequests.Load()
	success := m.successCount.Load()
	failure := m.failureCount.Load()
	if total != success+failure {
		t.Errorf("counter invariant broken: total=%d success=%d failure=%d", total, success, failure)
	}
}

// ── rpsWindow ────────────────────────────────────────────────────────────────

func TestRpsWindow_ZeroBeforeRecord(t *testing.T) {
	var w rpsWindow
	if r := w.rate(); r != 0 {
		t.Errorf("want 0 before any records, got %f", r)
	}
}

func TestRpsWindow_RecordAndRate(t *testing.T) {
	var w rpsWindow
	// Back-fill the previous second's bucket so rate() sees a completed second.
	prev := time.Now().Unix() - 1
	slot := int(prev % rpsWindowSize)
	w.seconds[slot] = prev
	w.buckets[slot] = 50

	r := w.rate()
	if r <= 0 {
		t.Errorf("want rate > 0 after recording, got %f", r)
	}
}

func TestRpsWindow_ClearsStaleSlot(t *testing.T) {
	var w rpsWindow
	// Write a bucket from 100 seconds ago — well outside the window.
	old := time.Now().Unix() - 100
	slot := int(old % rpsWindowSize)
	w.seconds[slot] = old
	w.buckets[slot] = 999

	if r := w.rate(); r != 0 {
		t.Errorf("stale bucket must not affect rate, got %f", r)
	}
}

func TestRpsWindow_RecordResetsStaleSlot(t *testing.T) {
	var w rpsWindow
	// Pre-load a slot with a stale second.
	now := time.Now().Unix()
	slot := int(now % rpsWindowSize)
	w.seconds[slot] = now - 1000 // very old
	w.buckets[slot] = 999

	// record() for the current second must reset the stale data.
	w.record(1)

	w.mu.Lock()
	if w.seconds[slot] != now {
		t.Errorf("record() should have reset seconds[%d] to %d, got %d", slot, now, w.seconds[slot])
	}
	if w.buckets[slot] != 1 {
		t.Errorf("record() should have reset buckets[%d] to 1, got %d", slot, w.buckets[slot])
	}
	w.mu.Unlock()
}

func TestRpsWindow_AccumulatesWithinSameSecond(t *testing.T) {
	var w rpsWindow
	w.record(10)
	w.record(5)
	w.record(3)

	now := time.Now().Unix()
	slot := int(now % rpsWindowSize)

	w.mu.Lock()
	got := w.buckets[slot]
	w.mu.Unlock()

	if got != 18 {
		t.Errorf("multiple record() calls in same second: want 18, got %d", got)
	}
}

func TestRpsWindow_RateAveragesOverWindow(t *testing.T) {
	var w rpsWindow
	now := time.Now().Unix()

	// Fill the two completed-second slots (age 1 and age 2).
	for age := int64(1); age < rpsWindowSize; age++ {
		sec := now - age
		s := int(sec % rpsWindowSize)
		w.seconds[s] = sec
		w.buckets[s] = 100 // 100 req/s each
	}

	rate := w.rate()
	// Both slots have 100 req; divided by (rpsWindowSize-1)=2 → expect 100.
	if rate != 100 {
		t.Errorf("rate: want 100.0, got %f", rate)
	}
}
