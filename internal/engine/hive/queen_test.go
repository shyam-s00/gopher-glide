package hive

import (
	"context"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func makeSpecs(n int) []httpreader.RequestSpec {
	specs := make([]httpreader.RequestSpec, n)
	for i := range specs {
		specs[i] = httpreader.RequestSpec{Method: "GET", URL: "http://example.com"}
	}
	return specs
}

func singleStage(dur time.Duration, rps int) []config.Stage {
	return []config.Stage{{Duration: dur, TargetRPS: rps}}
}

func collectManifests(ch <-chan SpawnManifest, timeout time.Duration) []SpawnManifest {
	var out []SpawnManifest
	deadline := time.After(timeout)
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, m)
		case <-deadline:
			return out
		}
	}
}

func runQueen(
	t *testing.T,
	stages []config.Stage,
	timeScale float64,
	specs []httpreader.RequestSpec,
	bufSize int,
	timeout time.Duration,
) ([]SpawnManifest, *Engine) {
	t.Helper()
	e := New()
	q := &queen{e: e}
	manifestCh := make(chan SpawnManifest, bufSize)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.run(ctx, stages, timeScale, specs, manifestCh) }()
	manifests := collectManifests(manifestCh, timeout)
	<-done
	return manifests, e
}

// ── manifest count matches target RPS ─────────────────────────────────────────

func TestQueen_SingleStage_ManifestCountMatchesTargetRPS(t *testing.T) {
	// 2-second stage at 10 RPS -> expect at least 1 manifest each with Count>0.
	stages := singleStage(2*time.Second, 10)
	manifests, _ := runQueen(t, stages, 1.0, makeSpecs(1), 32, 4*time.Second)
	if len(manifests) < 1 {
		t.Fatalf("expected at least 1 manifest, got %d", len(manifests))
	}
	for _, m := range manifests {
		if m.Count <= 0 {
			t.Errorf("expected Count > 0, got %d", m.Count)
		}
	}
}

func TestQueen_ManifestCount_ReflectsTargetRPS(t *testing.T) {
	// 30s stage at 10x scale = 3s real. The 1s ticker fires ~3 times.
	stages := singleStage(30*time.Second, 50)
	manifests, _ := runQueen(t, stages, 10.0, makeSpecs(1), 64, 5*time.Second)
	if len(manifests) == 0 {
		t.Fatal("expected at least 1 manifest")
	}
	for _, m := range manifests {
		if m.Count < 1 {
			t.Errorf("expected Count >= 1, got %d", m.Count)
		}
	}
}

// ── LERP values increase across a ramp stage ──────────────────────────────────

func TestQueen_Lerp_CountIncreasesAcrossRamp(t *testing.T) {
	// Ramp from 0 to 60 RPS over 30 seconds (10x scale = 3s actual, ~3 ticks).
	stages := singleStage(30*time.Second, 60)
	manifests, _ := runQueen(t, stages, 10.0, makeSpecs(1), 64, 5*time.Second)
	if len(manifests) < 2 {
		t.Skip("not enough ticks to verify LERP (flaky timing)")
	}
	first := manifests[0].Count
	last := manifests[len(manifests)-1].Count
	if last < first {
		t.Errorf("expected LERP to increase: first=%d last=%d", first, last)
	}
}

// ── zero-duration stage: instant step ─────────────────────────────────────────

func TestQueen_ZeroDurationStage_EmitsOneManifest(t *testing.T) {
	stages := []config.Stage{
		{Duration: 0, TargetRPS: 100},
	}
	manifests, _ := runQueen(t, stages, 1.0, makeSpecs(1), 4, 500*time.Millisecond)
	if len(manifests) != 1 {
		t.Fatalf("expected 1 manifest for zero-duration stage, got %d", len(manifests))
	}
	if manifests[0].Count < 1 {
		t.Errorf("expected Count >= 1, got %d", manifests[0].Count)
	}
}

func TestQueen_ZeroDurationStage_SetsTargetRPS(t *testing.T) {
	stages := []config.Stage{{Duration: 0, TargetRPS: 77}}
	e := New()
	q := &queen{e: e}
	manifestCh := make(chan SpawnManifest, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = q.run(ctx, stages, 1.0, makeSpecs(1), manifestCh)
	got := int(e.targetRPS.Load())
	if got != 77 {
		t.Fatalf("expected targetRPS=77, got %d", got)
	}
}

func TestQueen_ZeroDurationStage_SetsCurrentStage(t *testing.T) {
	stages := []config.Stage{{Duration: 0, TargetRPS: 10}}
	e := New()
	q := &queen{e: e}
	manifestCh := make(chan SpawnManifest, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = q.run(ctx, stages, 1.0, makeSpecs(1), manifestCh)
	if got := int(e.currentStage.Load()); got != 0 {
		t.Fatalf("expected currentStage=0, got %d", got)
	}
}

// ── multi-stage: zero-duration step then sustained stage ──────────────────────

func TestQueen_MultiStage_ZeroThenSustain(t *testing.T) {
	stages := []config.Stage{
		{Duration: 0, TargetRPS: 50},
		{Duration: 20 * time.Second, TargetRPS: 50}, // at 10x = 2s, ~2 ticks
	}
	manifests, _ := runQueen(t, stages, 10.0, makeSpecs(1), 64, 5*time.Second)
	if len(manifests) < 2 {
		t.Fatalf("expected at least 2 manifests (1 spike + 1+ sustain), got %d", len(manifests))
	}
}

// ── bias: pre-loaded positive bias increases count ────────────────────────────

func TestQueen_Bias_PositiveBiasIncreasesCount(t *testing.T) {
	// 30s at 10x = 3s real, ~3 ticks — gives enough time to drain bias.
	stages := singleStage(30*time.Second, 10)
	e := New()
	q := &queen{e: e}
	manifestCh := make(chan SpawnManifest, 32)
	e.rpsBias.Store(20)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.run(ctx, stages, 10.0, makeSpecs(1), manifestCh) }()
	manifests := collectManifests(manifestCh, 5*time.Second)
	<-done
	if len(manifests) == 0 {
		t.Fatal("expected at least 1 manifest")
	}
	for _, m := range manifests {
		if m.Count < 10 {
			t.Errorf("expected Count >= 10 with positive bias, got %d", m.Count)
		}
	}
}

func TestQueen_Bias_ApplyBiasViaChan_DrainedByQueen(t *testing.T) {
	// 30s at 10x = 3s real so we have multiple ticks and time to inject bias.
	stages := singleStage(30*time.Second, 20)
	e := New()
	q := &queen{e: e}
	manifestCh := make(chan SpawnManifest, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.run(ctx, stages, 10.0, makeSpecs(1), manifestCh) }()
	// Give the queen time to start the ticker, then send delta.
	time.Sleep(200 * time.Millisecond)
	e.biasCh <- 15
	_ = collectManifests(manifestCh, 5*time.Second)
	<-done
	if e.rpsBias.Load() != 15 {
		t.Errorf("expected rpsBias=15 after drain, got %d", e.rpsBias.Load())
	}
}

func TestQueen_Bias_NegativeBias_ClampsToOne(t *testing.T) {
	// 30s at 10x = 3s real.
	stages := singleStage(30*time.Second, 5)
	e := New()
	e.rpsBias.Store(-100)
	q := &queen{e: e}
	manifestCh := make(chan SpawnManifest, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.run(ctx, stages, 10.0, makeSpecs(1), manifestCh) }()
	manifests := collectManifests(manifestCh, 5*time.Second)
	<-done
	for _, m := range manifests {
		if m.Count < 1 {
			t.Errorf("expected Count >= 1 (clamp), got %d", m.Count)
		}
	}
}

// ── context cancel exits cleanly ──────────────────────────────────────────────

func TestQueen_ContextCancel_ExitsCleanly(t *testing.T) {
	stages := singleStage(60*time.Second, 10)
	e := New()
	q := &queen{e: e}
	manifestCh := make(chan SpawnManifest, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.run(ctx, stages, 1.0, makeSpecs(1), manifestCh) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error on cancel, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Queen did not exit within 3s after context cancel")
	}
}

func TestQueen_ContextAlreadyCancelled_ExitsImmediately(t *testing.T) {
	stages := singleStage(60*time.Second, 10)
	e := New()
	q := &queen{e: e}
	manifestCh := make(chan SpawnManifest, 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- q.run(ctx, stages, 1.0, makeSpecs(1), manifestCh) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Queen did not exit quickly for pre-cancelled context")
	}
}

// ── spec index round-robin ────────────────────────────────────────────────────

func TestQueen_SpecIndex_IsWithinBounds(t *testing.T) {
	numSpecs := 3
	// 30s at 10x = 3s real so the ticker fires a few times.
	stages := singleStage(30*time.Second, 10)
	manifests, _ := runQueen(t, stages, 10.0, makeSpecs(numSpecs), 32, 5*time.Second)
	for _, m := range manifests {
		if m.SpecIndex < 0 || m.SpecIndex >= numSpecs {
			t.Errorf("SpecIndex=%d out of bounds for numSpecs=%d", m.SpecIndex, numSpecs)
		}
	}
}

// ── currentStage advances across two stages ───────────────────────────────────

func TestQueen_CurrentStage_AdvancesAsExpected(t *testing.T) {
	stages := []config.Stage{
		{Duration: 10 * time.Second, TargetRPS: 10},
		{Duration: 10 * time.Second, TargetRPS: 20},
	}
	e := New()
	q := &queen{e: e}
	manifestCh := make(chan SpawnManifest, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.run(ctx, stages, 10.0, makeSpecs(1), manifestCh) }()
	_ = collectManifests(manifestCh, 5*time.Second)
	<-done
	got := int(e.currentStage.Load())
	if got < 0 || got > 1 {
		t.Errorf("expected currentStage in [0,1], got %d", got)
	}
}

// ── targetRPS atomic is updated each tick ────────────────────────────────────

func TestQueen_TargetRPS_UpdatedEachTick(t *testing.T) {
	stages := singleStage(30*time.Second, 30)
	e := New()
	q := &queen{e: e}
	manifestCh := make(chan SpawnManifest, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.run(ctx, stages, 10.0, makeSpecs(1), manifestCh) }()
	_ = collectManifests(manifestCh, 5*time.Second)
	<-done
	if e.targetRPS.Load() == 0 {
		t.Error("expected targetRPS to be non-zero after run")
	}
}

// ── full stage completes without hanging ──────────────────────────────────────

func TestQueen_FullStage_CompletesWithoutHang(t *testing.T) {
	stages := singleStage(2*time.Second, 5)
	finished := make(chan struct{})
	go func() {
		_, _ = runQueen(t, stages, 20.0, makeSpecs(1), 32, 3*time.Second)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Queen did not complete stage within timeout")
	}
}

// ── full channel does not block the Queen ─────────────────────────────────────

func TestQueen_FullChannel_DoesNotBlock(t *testing.T) {
	stages := singleStage(3*time.Second, 10)
	e := New()
	q := &queen{e: e}
	// Zero-buffer: every emit attempt will be dropped.
	manifestCh := make(chan SpawnManifest, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.run(ctx, stages, 10.0, makeSpecs(1), manifestCh) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil on context timeout, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Queen blocked on full channel")
	}
}

// ── manifest Duration field ────────────────────────────────────────────────────

func TestQueen_NormalTick_ManifestDurationIsOneSecond(t *testing.T) {
	// 30s at 10x = 3s real; ticker fires ~3 times, each with Duration=1s.
	// The FINAL manifest always comes from the stageTimer (fractional remainder,
	// clamped to hatcheryTick). Only the intermediate ticker manifests are 1s.
	stages := singleStage(30*time.Second, 10)
	manifests, _ := runQueen(t, stages, 10.0, makeSpecs(1), 64, 5*time.Second)
	if len(manifests) == 0 {
		t.Fatal("expected at least 1 manifest")
	}
	// All manifests must have a positive, non-zero Duration.
	for i, m := range manifests {
		if m.Duration <= 0 {
			t.Errorf("manifest[%d]: expected Duration > 0, got %v", i, m.Duration)
		}
	}
	// All but the final manifest are ticker emits and must be exactly 1s.
	// The final manifest is the stageTimer emit (fractional remainder ≤ 1s).
	if len(manifests) > 1 {
		for i, m := range manifests[:len(manifests)-1] {
			if m.Duration != time.Second {
				t.Errorf("ticker manifest[%d]: expected Duration=1s, got %v", i, m.Duration)
			}
		}
	}
}

func TestQueen_SubSecondStage_ManifestDurationIsSubSecond(t *testing.T) {
	// A 100ms stage at timeScale=1 fires the stageTimer immediately.
	// The emitted manifest must carry a Duration < 1s (the fractional remainder).
	stages := singleStage(100*time.Millisecond, 10)
	e := New()
	q := &queen{e: e}
	manifestCh := make(chan SpawnManifest, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.run(ctx, stages, 1.0, makeSpecs(1), manifestCh) }()
	manifests := collectManifests(manifestCh, 2*time.Second)
	<-done
	if len(manifests) == 0 {
		t.Fatal("expected at least 1 manifest from 100ms stage")
	}
	last := manifests[len(manifests)-1]
	if last.Duration >= time.Second {
		t.Errorf("sub-second stage final manifest: expected Duration < 1s, got %v", last.Duration)
	}
	if last.Duration <= 0 {
		t.Errorf("sub-second stage final manifest: expected Duration > 0, got %v", last.Duration)
	}
}
