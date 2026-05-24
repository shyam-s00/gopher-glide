package hive

import (
	"sync"
	"testing"
)

// ── ApplyBias ─────────────────────────────────────────────────────────────────

func TestApplyBias_SendsDeltaToBiasCh(t *testing.T) {
	e := New()
	e.ApplyBias(10)
	select {
	case got := <-e.biasCh:
		if got != 10 {
			t.Fatalf("expected delta=10, got %d", got)
		}
	default:
		t.Fatal("expected a value in biasCh, channel was empty")
	}
}

func TestApplyBias_NegativeDelta(t *testing.T) {
	e := New()
	e.ApplyBias(-5)
	select {
	case got := <-e.biasCh:
		if got != -5 {
			t.Fatalf("expected delta=-5, got %d", got)
		}
	default:
		t.Fatal("expected a value in biasCh, channel was empty")
	}
}

func TestApplyBias_ZeroDelta(t *testing.T) {
	e := New()
	e.ApplyBias(0)
	select {
	case got := <-e.biasCh:
		if got != 0 {
			t.Fatalf("expected delta=0, got %d", got)
		}
	default:
		t.Fatal("expected a value in biasCh, channel was empty")
	}
}

func TestApplyBias_DropWhenChannelFull(t *testing.T) {
	e := New() // biasCh capacity = 16
	// Fill the channel to capacity.
	for i := 0; i < 16; i++ {
		e.ApplyBias(1)
	}
	// This 17th send must not block and must be silently dropped.
	e.ApplyBias(99)

	// Drain all 16 slots — the dropped value must not appear.
	count := 0
	for {
		select {
		case v := <-e.biasCh:
			if v == 99 {
				t.Fatal("dropped value 99 must not appear in channel")
			}
			count++
		default:
			goto done
		}
	}
done:
	if count != 16 {
		t.Fatalf("expected 16 values in channel, got %d", count)
	}
}

func TestApplyBias_ConcurrentSenders_NoDeadlock(t *testing.T) {
	e := New()
	var wg sync.WaitGroup
	// 64 concurrent senders — only 16 will land; rest are dropped silently.
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(delta int) {
			defer wg.Done()
			e.ApplyBias(delta)
		}(i)
	}
	wg.Wait() // must complete without deadlock
	// Drain whatever landed.
	for {
		select {
		case <-e.biasCh:
		default:
			return
		}
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
	// Simulate Queen draining and accumulating bias deltas.
	e.rpsBias.Add(10)
	e.rpsBias.Add(5)
	e.rpsBias.Add(-3)
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
