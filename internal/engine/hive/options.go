package hive

import (
	"net/http"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// WithHTTPClient replaces the default shared HTTP client with the provided one.
//
// Primarily useful in tests to inject an httptest.Server-backed client without
// going through the network stack. Production callers should prefer the default
// client built by New(), which is tuned via WithPeakRPS / buildTransport.
func WithHTTPClient(c *http.Client) EngineOption {
	return func(e *Engine) { e.client = c }
}

// WithRecorder attaches a snap.Recorder to the engine.
//
// When set, every HTTP response is passed to recorder.Record() after the
// body is drained. The hot-path is a single nil-check — zero overhead when
// snapping is off (the default).
func WithRecorder(r snap.Recorder) EngineOption {
	return func(e *Engine) { e.recorder = r }
}

// WithSampleRate sets the fraction of responses whose body is captured for
// schema inference (0.0–1.0). Default is 0.05 (5 %).
//
// The rate is rounded to the nearest 1-in-N integer slot:
//
//	0.05  → sampleEvery = 20  (capture 1-in-20)
//	0.10  → sampleEvery = 10  (capture 1-in-10)
//	0.25  → sampleEvery = 4   (capture 1-in-4)
//	1.0   → sampleEvery = 1   (capture every response)
//	≤ 0   → sampleEvery = 0   (body capture disabled)
func WithSampleRate(rate float64) EngineOption {
	return func(e *Engine) {
		if rate <= 0 {
			e.sampleEvery = 0 // disable body capture entirely
			return
		}
		if rate >= 1 {
			e.sampleEvery = 1 // capture every response
			return
		}
		n := int(1.0 / rate)
		if n < 1 {
			n = 1
		}
		e.sampleEvery = n
	}
}

// shouldSample returns true for 1-in-sampleEvery requests.
//
// Uses an atomic counter so it is safe to call from many Actor goroutines
// concurrently without any locking. sampleEvery == 0 disables body capture.
func (e *Engine) shouldSample() bool {
	if e.sampleEvery == 0 {
		return false
	}
	return e.sampleCount.Add(1)%int64(e.sampleEvery) == 0
}
