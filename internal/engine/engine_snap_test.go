package engine

import (
	"testing"
)

func TestNew_DefaultSampleEvery(t *testing.T) {
	e := New()
	if e.sampleEvery != 20 {
		t.Errorf("default sampleEvery = %d, want 20", e.sampleEvery)
	}
	if e.recorder != nil {
		t.Error("default recorder should be nil")
	}
}

func TestWithSampleRate(t *testing.T) {
	cases := []struct {
		rate        float64
		wantEvery   int
		wantDisable bool
	}{
		{0.05, 20, false},
		{0.10, 10, false},
		{0.25, 4, false},
		{1.00, 1, false},
		{0.00, 0, true}, // disabled
		{-1.0, 0, true}, // disabled
	}
	for _, c := range cases {
		e := New(WithSampleRate(c.rate))
		if c.wantDisable {
			if e.sampleEvery != 0 {
				t.Errorf("rate=%.2f: sampleEvery = %d, want 0 (disabled)", c.rate, e.sampleEvery)
			}
		} else {
			if e.sampleEvery != c.wantEvery {
				t.Errorf("rate=%.2f: sampleEvery = %d, want %d", c.rate, e.sampleEvery, c.wantEvery)
			}
		}
	}
}

func TestShouldSample_Frequency(t *testing.T) {
	e := New(WithSampleRate(0.05)) // 1-in-20

	sampled := 0
	const total = 1000
	for i := 0; i < total; i++ {
		if e.shouldSample() {
			sampled++
		}
	}
	// Expect exactly total/20 = 50 samples.
	if sampled != total/20 {
		t.Errorf("sampled %d/%d, want %d (1-in-20)", sampled, total, total/20)
	}
}

func TestShouldSample_Disabled(t *testing.T) {
	e := New(WithSampleRate(0))
	for i := 0; i < 100; i++ {
		if e.shouldSample() {
			t.Error("shouldSample should always return false when sampleEvery == 0")
		}
	}
}

func TestShouldSample_Every(t *testing.T) {
	e := New(WithSampleRate(1.0)) // capture every response
	for i := 0; i < 10; i++ {
		if !e.shouldSample() {
			t.Error("shouldSample should always return true when sampleEvery == 1")
		}
	}
}

func TestWithRecorder_NilByDefault(t *testing.T) {
	e := New()
	if e.recorder != nil {
		t.Error("recorder should be nil by default")
	}
}
