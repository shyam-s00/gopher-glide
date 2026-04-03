package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

func newFastTestServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestWithRecorder_AttachesRecorder(t *testing.T) {
	rec := snap.NewDefaultRecorder(64)
	e := New(WithRecorder(rec))
	if e.recorder == nil {
		t.Fatal("WithRecorder should attach the recorder to the engine")
	}
	_, _ = rec.Finalize(snap.RunMeta{StartTime: time.Now(), EndTime: time.Now()})
}

func TestWithRecorder_Nil_NoPanic(t *testing.T) {
	e := New(WithRecorder(nil))
	if e.recorder != nil {
		t.Error("WithRecorder(nil) should leave recorder nil")
	}
}

func TestSetTargetRPS(t *testing.T) {
	e := New()
	e.SetTargetRPS(200)
	if got := e.targetRPS.Load(); got != 200 {
		t.Errorf("targetRPS: want 200, got %d", got)
	}
}

func TestSetTargetRPS_Zero(t *testing.T) {
	e := New()
	e.SetTargetRPS(100)
	e.SetTargetRPS(0)
	if got := e.targetRPS.Load(); got != 0 {
		t.Errorf("targetRPS after reset: want 0, got %d", got)
	}
}

func TestGetStartTime_ZeroBeforeRun(t *testing.T) {
	e := New()
	if !e.GetStartTime().IsZero() {
		t.Error("GetStartTime should return zero time before RunStages is called")
	}
}

func TestGetEndTime_ReturnsNowWhenNotFinished(t *testing.T) {
	e := New()
	before := time.Now()
	got := e.GetEndTime()
	after := time.Now()
	if got.Before(before) || got.After(after.Add(time.Second)) {
		t.Errorf("GetEndTime before run: expected approximately now, got %v", got)
	}
}

func TestGetStartTime_SetAfterRun(t *testing.T) {
	srv, _ := newFastTestServer(t)
	e := New()
	cfg := &config.Config{
		ConfigSection: config.Section{TimeScale: 100.0},
		Stages:        []config.Stage{{Duration: 100 * time.Millisecond, TargetRPS: 1}},
	}
	specs := []httpreader.RequestSpec{{Method: "GET", URL: srv.URL}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	before := time.Now()
	if err := e.RunStages(ctx, cfg, specs); err != nil {
		t.Fatalf("RunStages: %v", err)
	}
	after := time.Now()
	start := e.GetStartTime()
	if start.IsZero() {
		t.Fatal("GetStartTime should be non-zero after RunStages")
	}
	if start.Before(before) || start.After(after) {
		t.Errorf("GetStartTime %v not in expected range [%v, %v]", start, before, after)
	}
}

func TestGetEndTime_SetAfterRun(t *testing.T) {
	srv, _ := newFastTestServer(t)
	e := New()
	cfg := &config.Config{
		ConfigSection: config.Section{TimeScale: 100.0},
		Stages:        []config.Stage{{Duration: 100 * time.Millisecond, TargetRPS: 1}},
	}
	specs := []httpreader.RequestSpec{{Method: "GET", URL: srv.URL}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.RunStages(ctx, cfg, specs); err != nil {
		t.Fatalf("RunStages: %v", err)
	}
	end := e.GetEndTime()
	if end.IsZero() {
		t.Fatal("GetEndTime should be non-zero after RunStages")
	}
	if end.Before(e.GetStartTime()) {
		t.Errorf("GetEndTime %v should be after GetStartTime %v", end, e.GetStartTime())
	}
}

func TestWithSampleRate_ZeroDisablesCapture(t *testing.T) {
	e := New(WithSampleRate(0))
	if e.sampleEvery != 0 {
		t.Errorf("sampleEvery: want 0 (disabled), got %d", e.sampleEvery)
	}
}

func TestWithSampleRate_OneCapturesEvery(t *testing.T) {
	e := New(WithSampleRate(1.0))
	if e.sampleEvery != 1 {
		t.Errorf("sampleEvery: want 1, got %d", e.sampleEvery)
	}
}

func TestWithSampleRate_FivePercent(t *testing.T) {
	e := New(WithSampleRate(0.05))
	if e.sampleEvery != 20 {
		t.Errorf("sampleEvery for 5pct: want 20, got %d", e.sampleEvery)
	}
}

func TestWithSampleRate_TenPercent(t *testing.T) {
	e := New(WithSampleRate(0.1))
	if e.sampleEvery != 10 {
		t.Errorf("sampleEvery for 10pct: want 10, got %d", e.sampleEvery)
	}
}

func TestShouldSample_DisabledAlwaysFalse(t *testing.T) {
	e := New(WithSampleRate(0))
	for i := 0; i < 100; i++ {
		if e.shouldSample() {
			t.Error("shouldSample should always return false when sampleEvery=0")
		}
	}
}

func TestShouldSample_OneAlwaysTrue(t *testing.T) {
	e := New(WithSampleRate(1.0))
	for i := 0; i < 10; i++ {
		if !e.shouldSample() {
			t.Errorf("shouldSample should always return true when sampleEvery=1 (iter %d)", i)
		}
	}
}

func TestShouldSample_CorrectFrequency(t *testing.T) {
	e := New(WithSampleRate(0.5))
	hits := 0
	for i := 0; i < 100; i++ {
		if e.shouldSample() {
			hits++
		}
	}
	if hits < 40 || hits > 60 {
		t.Errorf("shouldSample at 50pct rate: expected ~50 hits out of 100, got %d", hits)
	}
}
