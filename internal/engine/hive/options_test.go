package hive

import (
	"testing"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// ── stub recorder ─────────────────────────────────────────────────────────────

// stubRecorder is a minimal snap.Recorder for testing that just counts calls.
type stubRecorder struct {
	calls int
}

func (r *stubRecorder) Record(_ snap.RecordEntry)                       { r.calls++ }
func (r *stubRecorder) Finalize(_ snap.RunMeta) (*snap.Snapshot, error) { return nil, nil }

// ── WithRecorder ──────────────────────────────────────────────────────────────

func TestWithRecorder_AttachesRecorder(t *testing.T) {
	rec := &stubRecorder{}
	e := New(WithRecorder(rec))
	if e.recorder == nil {
		t.Fatal("expected recorder to be set, got nil")
	}
	if e.recorder != rec {
		t.Fatal("expected recorder to be the exact stub instance")
	}
}

func TestWithRecorder_NilRecorder(t *testing.T) {
	// nil recorder is valid — disables snap (same as default)
	e := New(WithRecorder(nil))
	if e.recorder != nil {
		t.Fatal("expected recorder to be nil when passed nil")
	}
}

func TestWithRecorder_DefaultIsNil(t *testing.T) {
	e := New()
	if e.recorder != nil {
		t.Fatal("expected recorder=nil by default (snap disabled)")
	}
}

func TestWithRecorder_OverridesPreviousOption(t *testing.T) {
	first := &stubRecorder{}
	second := &stubRecorder{}
	e := New(WithRecorder(first), WithRecorder(second))
	if e.recorder != second {
		t.Fatal("last WithRecorder option must win")
	}
}

// ── WithSampleRate ────────────────────────────────────────────────────────────

func TestWithSampleRate_Default_Is5Percent(t *testing.T) {
	e := New() // no option → default 5 %
	if e.sampleEvery != 20 {
		t.Fatalf("expected sampleEvery=20 (5%%), got %d", e.sampleEvery)
	}
}

func TestWithSampleRate_Zero_DisablesSampling(t *testing.T) {
	e := New(WithSampleRate(0))
	if e.sampleEvery != 0 {
		t.Fatalf("expected sampleEvery=0 (disabled), got %d", e.sampleEvery)
	}
}

func TestWithSampleRate_Negative_DisablesSampling(t *testing.T) {
	e := New(WithSampleRate(-0.5))
	if e.sampleEvery != 0 {
		t.Fatalf("expected sampleEvery=0 for negative rate, got %d", e.sampleEvery)
	}
}

func TestWithSampleRate_One_CapturesEveryResponse(t *testing.T) {
	e := New(WithSampleRate(1.0))
	if e.sampleEvery != 1 {
		t.Fatalf("expected sampleEvery=1 (100%%), got %d", e.sampleEvery)
	}
}

func TestWithSampleRate_GreaterThanOne_CapturesEveryResponse(t *testing.T) {
	e := New(WithSampleRate(2.0))
	if e.sampleEvery != 1 {
		t.Fatalf("expected sampleEvery=1 for rate>1, got %d", e.sampleEvery)
	}
}

func TestWithSampleRate_5Percent(t *testing.T) {
	e := New(WithSampleRate(0.05))
	if e.sampleEvery != 20 {
		t.Fatalf("expected sampleEvery=20 for 5%%, got %d", e.sampleEvery)
	}
}

func TestWithSampleRate_10Percent(t *testing.T) {
	e := New(WithSampleRate(0.10))
	if e.sampleEvery != 10 {
		t.Fatalf("expected sampleEvery=10 for 10%%, got %d", e.sampleEvery)
	}
}

func TestWithSampleRate_25Percent(t *testing.T) {
	e := New(WithSampleRate(0.25))
	if e.sampleEvery != 4 {
		t.Fatalf("expected sampleEvery=4 for 25%%, got %d", e.sampleEvery)
	}
}

func TestWithSampleRate_50Percent(t *testing.T) {
	e := New(WithSampleRate(0.50))
	if e.sampleEvery != 2 {
		t.Fatalf("expected sampleEvery=2 for 50%%, got %d", e.sampleEvery)
	}
}

// ── shouldSample ──────────────────────────────────────────────────────────────

func TestShouldSample_DisabledWhenSampleEveryZero(t *testing.T) {
	e := New(WithSampleRate(0))
	for i := 0; i < 100; i++ {
		if e.shouldSample() {
			t.Fatalf("shouldSample() must always return false when sampleEvery=0")
		}
	}
}

func TestShouldSample_EveryResponseWhenSampleEveryOne(t *testing.T) {
	e := New(WithSampleRate(1.0))
	for i := 0; i < 50; i++ {
		if !e.shouldSample() {
			t.Fatalf("shouldSample() must always return true when sampleEvery=1 (call %d)", i)
		}
	}
}

func TestShouldSample_Frequency_1In20(t *testing.T) {
	e := New(WithSampleRate(0.05)) // sampleEvery=20
	sampled := 0
	const total = 200
	for i := 0; i < total; i++ {
		if e.shouldSample() {
			sampled++
		}
	}
	// Expect exactly total/20 = 10 samples (deterministic — not probabilistic).
	expected := total / 20
	if sampled != expected {
		t.Fatalf("expected %d samples in %d calls (1-in-20), got %d", expected, total, sampled)
	}
}

func TestShouldSample_Frequency_1In10(t *testing.T) {
	e := New(WithSampleRate(0.10)) // sampleEvery=10
	sampled := 0
	const total = 100
	for i := 0; i < total; i++ {
		if e.shouldSample() {
			sampled++
		}
	}
	expected := total / 10
	if sampled != expected {
		t.Fatalf("expected %d samples in %d calls (1-in-10), got %d", expected, total, sampled)
	}
}

func TestShouldSample_Frequency_1In4(t *testing.T) {
	e := New(WithSampleRate(0.25)) // sampleEvery=4
	sampled := 0
	const total = 80
	for i := 0; i < total; i++ {
		if e.shouldSample() {
			sampled++
		}
	}
	expected := total / 4
	if sampled != expected {
		t.Fatalf("expected %d samples in %d calls (1-in-4), got %d", expected, total, sampled)
	}
}

func TestShouldSample_CounterIsAtomic_NoRace(t *testing.T) {
	// Concurrent calls to shouldSample must not race on sampleCount.
	e := New(WithSampleRate(0.05))
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				_ = e.shouldSample()
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
