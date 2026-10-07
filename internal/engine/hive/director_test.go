package hive

import (
	"sync"
	"testing"
)

// ── ApplyBias ─────────────────────────────────────────────────────────────────

func TestApplyBias_AccumulatesImmediately(t *testing.T) {
	e := New()
	e.ApplyBias(10)
	if got := e.GetBias(); got != 10 {
		t.Fatalf("expected bias=10 right after ApplyBias, got %d", got)
	}
	e.ApplyBias(-4)
	if got := e.GetBias(); got != 6 {
		t.Fatalf("expected cumulative bias=6, got %d", got)
	}
}

func TestApplyBias_NegativeDelta(t *testing.T) {
	e := New()
	e.ApplyBias(-5)
	if got := e.GetBias(); got != -5 {
		t.Fatalf("expected bias=-5, got %d", got)
	}
}

func TestApplyBias_ZeroDelta(t *testing.T) {
	e := New()
	e.ApplyBias(0)
	if got := e.GetBias(); got != 0 {
		t.Fatalf("expected bias=0, got %d", got)
	}
}

func TestApplyBias_NeverDrops(t *testing.T) {
	e := New()
	for i := 0; i < 1000; i++ {
		e.ApplyBias(1)
	}
	if got := e.GetBias(); got != 1000 {
		t.Fatalf("expected all 1000 deltas applied, got %d", got)
	}
}

func TestApplyBias_ConcurrentSenders(t *testing.T) {
	e := New()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.ApplyBias(2)
		}()
	}
	wg.Wait()
	if got := e.GetBias(); got != 128 {
		t.Fatalf("expected bias=128, got %d", got)
	}
}

// ── GetBias ───────────────────────────────────────────────────────────────────

func TestGetBias_ZeroInitially(t *testing.T) {
	e := New()
	if got := e.GetBias(); got != 0 {
		t.Fatalf("expected bias=0 initially, got %d", got)
	}
}

func TestGetBias_ReflectsRpsBiasAtomic(t *testing.T) {
	e := New()
	e.rpsBias.Store(42)
	if got := e.GetBias(); got != 42 {
		t.Fatalf("expected bias=42, got %d", got)
	}
}

func TestGetBias_NegativeBias(t *testing.T) {
	e := New()
	e.rpsBias.Store(-15)
	if got := e.GetBias(); got != -15 {
		t.Fatalf("expected bias=-15, got %d", got)
	}
}

func TestGetBias_ReflectsAccumulatedDeltas(t *testing.T) {
	e := New()
	e.ApplyBias(10)
	e.ApplyBias(5)
	e.ApplyBias(-3)
	if got := e.GetBias(); got != 12 {
		t.Fatalf("expected accumulated bias=12, got %d", got)
	}
}

// ── SetTargetRPS ──────────────────────────────────────────────────────────────

func TestSetTargetRPS_StoresValue(t *testing.T) {
	e := New()
	e.SetTargetRPS(500)
	if got := int(e.targetRPS.Load()); got != 500 {
		t.Fatalf("expected targetRPS=500, got %d", got)
	}
}

func TestSetTargetRPS_ZeroAllowed(t *testing.T) {
	e := New()
	e.SetTargetRPS(100)
	e.SetTargetRPS(0)
	if got := int(e.targetRPS.Load()); got != 0 {
		t.Fatalf("expected targetRPS=0, got %d", got)
	}
}

func TestSetTargetRPS_OverwritesPreviousValue(t *testing.T) {
	e := New()
	e.SetTargetRPS(100)
	e.SetTargetRPS(9999)
	if got := int(e.targetRPS.Load()); got != 9999 {
		t.Fatalf("expected targetRPS=9999, got %d", got)
	}
}

func TestSetTargetRPS_ReflectedInGetMetrics(t *testing.T) {
	e := New()
	e.SetTargetRPS(250)
	snap := e.GetMetrics()
	if snap.TargetRPS != 250 {
		t.Fatalf("expected MetricsSnapshot.TargetRPS=250, got %d", snap.TargetRPS)
	}
}

func TestSetTargetRPS_ConcurrentWrites_NoRace(t *testing.T) {
	e := New()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(rps int) {
			defer wg.Done()
			e.SetTargetRPS(rps)
		}(i * 10)
	}
	wg.Wait()
	// Just verify we can read back without panic.
	_ = e.GetMetrics().TargetRPS
}
