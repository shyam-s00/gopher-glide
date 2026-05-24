package hive_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine/hive"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// newSnapTestServer returns an httptest.Server that always responds 200 OK
// with a small JSON body, suitable for snap recording tests.
func newSnapTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestHiveSnapRecorderEndToEnd mirrors TestWithRecorder_AttachesRecorder from
// engine_extra_test.go but exercises a full RunStages call so that the
// recorder actually accumulates entries via the Actor hot-path.
//
// Assertions:
//   - Snapshot.Endpoints is non-empty.
//   - The first endpoint has a non-zero StatusCode distribution, non-empty URL
//     (endpoint ID), and at least one request recorded.
//   - EndpointSnap.Latency.P99 > 0 (a real request was timed).
func TestHiveSnapRecorderEndToEnd(t *testing.T) {
	srv := newSnapTestServer(t)

	rec := snap.NewDefaultRecorder(0) // 0 → use DefaultMaxBodySamples
	e := hive.New(
		hive.WithRecorder(rec),
		hive.WithSampleRate(1.0), // capture every response body
	)

	cfg := &config.Config{
		ConfigSection: config.Section{TimeScale: 1.0},
		Stages: []config.Stage{
			// 500ms is intentional: sub-second stages only trigger the Queen's
			// stageTimer (fired at stageStart+500ms), which always emits exactly
			// one SpawnManifest. A 1-second stage would create a race between the
			// Queen's 1-second heartbeat ticker and the stageTimer — both fire
			// at ~t=1s, and Go's non-deterministic select might pick the ticker
			// case that does `break stageLoop` without emitting, leaving zero actors.
			{Duration: 500 * time.Millisecond, TargetRPS: 5},
		},
	}
	specs := []httpreader.RequestSpec{{Method: "GET", URL: srv.URL}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.RunStages(ctx, cfg, specs); err != nil {
		t.Fatalf("RunStages: %v", err)
	}

	snapData, err := rec.Finalize(snap.RunMeta{
		StartTime: e.GetStartTime(),
		EndTime:   e.GetEndTime(),
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// ── assert snapshot has content ───────────────────────────────────────
	if len(snapData.Endpoints) == 0 {
		t.Fatal("expected at least one endpoint in snapshot, got none")
	}

	ep := snapData.Endpoints[0]

	if ep.ID == "" {
		t.Error("EndpointSnap.ID (URL) should be non-empty")
	}

	if ep.RequestCount == 0 {
		t.Error("EndpointSnap.RequestCount should be > 0")
	}

	hasNonZeroStatus := false
	for _, frac := range ep.StatusDist {
		if frac > 0 {
			hasNonZeroStatus = true
			break
		}
	}
	if !hasNonZeroStatus {
		t.Error("EndpointSnap.StatusDist should contain at least one non-zero entry")
	}

	if ep.Latency.P99 < 0 {
		t.Errorf("EndpointSnap.Latency.P99 should be >= 0, got %v", ep.Latency.P99)
	}
}

// TestHiveProfileShapedMultiStage verifies that the Hive engine correctly
// handles a profile-shaped 3-stage config (ramp-up → sustain → ramp-down),
// mirroring what profile.InflateSegments produces at runtime.
//
// TimeScale: 100 compresses each 2-second logical stage to ~20ms wall time so
// the test completes in ~60ms. At that scale every stage duration is well below
// 1 second, meaning each stage triggers only the Queen's stageTimer (not the
// heartbeat ticker), which guarantees exactly one SpawnManifest per stage.
//
// Assertions:
//   - GetMetrics().TotalRequests > 0  (actors executed HTTP requests)
//   - GetMetrics().TotalStages == 3   (engine tracked all three stages)
func TestHiveProfileShapedMultiStage(t *testing.T) {
	srv := newSnapTestServer(t)

	e := hive.New()

	// Three-stage profile: ramp-up (0→5 RPS), sustain (5 RPS), ramp-down (5→1 RPS).
	// TimeScale:100 → scaledDur = 2s/100 = 20ms per stage (total ~60ms wall time).
	cfg := &config.Config{
		ConfigSection: config.Section{TimeScale: 100.0},
		Stages: []config.Stage{
			{Duration: 2 * time.Second, TargetRPS: 5}, // ramp-up
			{Duration: 2 * time.Second, TargetRPS: 5}, // sustain
			{Duration: 2 * time.Second, TargetRPS: 1}, // ramp-down
		},
	}
	specs := []httpreader.RequestSpec{{Method: "GET", URL: srv.URL}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.RunStages(ctx, cfg, specs); err != nil {
		t.Fatalf("RunStages: %v", err)
	}

	m := e.GetMetrics()

	if m.TotalRequests == 0 {
		t.Error("expected TotalRequests > 0 after multi-stage run")
	}
	if m.TotalStages != 3 {
		t.Errorf("expected TotalStages == 3, got %d", m.TotalStages)
	}
}
