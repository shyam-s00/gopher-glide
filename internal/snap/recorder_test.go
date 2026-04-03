package snap

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
)

func TestDefaultRecorder_SingleEndpoint(t *testing.T) {
	r := NewDefaultRecorder(256)

	r.Record(RecordEntry{
		Timestamp:  time.Now(),
		Method:     "GET",
		URL:        "/api/users",
		StatusCode: 200,
		Duration:   10 * time.Millisecond,
	})
	r.Record(RecordEntry{
		Timestamp:  time.Now(),
		Method:     "GET",
		URL:        "/api/users",
		StatusCode: 500,
		Duration:   50 * time.Millisecond,
		Error:      fmt.Errorf("server error"),
	})

	snap, err := r.Finalize(RunMeta{
		Tag:       "test-v1",
		StartTime: time.Now().Add(-time.Minute),
		EndTime:   time.Now(),
		PeakRPS:   10,
	})
	if err != nil {
		t.Fatalf("Finalize returned error: %v", err)
	}

	if snap.Version != 1 {
		t.Errorf("expected version 1, got %d", snap.Version)
	}
	if snap.Meta.Tag != "test-v1" {
		t.Errorf("expected tag test-v1, got %q", snap.Meta.Tag)
	}
	if snap.Meta.TotalRequests != 2 {
		t.Errorf("expected 2 total requests, got %d", snap.Meta.TotalRequests)
	}
	if len(snap.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(snap.Endpoints))
	}

	ep := snap.Endpoints[0]
	if ep.ID != "GET:/api/users" {
		t.Errorf("unexpected endpoint ID %q", ep.ID)
	}
	if ep.RequestCount != 2 {
		t.Errorf("expected RequestCount 2, got %d", ep.RequestCount)
	}
	if ep.ErrorRate != 0.5 {
		t.Errorf("expected ErrorRate 0.5, got %f", ep.ErrorRate)
	}
	if ep.StatusDist["200"] != 0.5 {
		t.Errorf("expected 200 dist 0.5, got %f", ep.StatusDist["200"])
	}
	if ep.StatusDist["500"] != 0.5 {
		t.Errorf("expected 500 dist 0.5, got %f", ep.StatusDist["500"])
	}
}

func TestDefaultRecorder_MultipleEndpoints(t *testing.T) {
	r := NewDefaultRecorder(0) // exercises default buffer size

	endpoints := []string{"/api/users", "/api/orders", "/api/products"}
	for _, ep := range endpoints {
		r.Record(RecordEntry{
			Method:     "GET",
			URL:        ep,
			StatusCode: 200,
			Duration:   5 * time.Millisecond,
		})
	}

	snap, err := r.Finalize(RunMeta{StartTime: time.Now(), EndTime: time.Now()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snap.Endpoints) != 3 {
		t.Errorf("expected 3 endpoints, got %d", len(snap.Endpoints))
	}
}

func TestDefaultRecorder_ConcurrentSafety(t *testing.T) {
	r := NewDefaultRecorder(8192)
	const goroutines = 50
	const entriesEach = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			for j := 0; j < entriesEach; j++ {
				r.Record(RecordEntry{
					Method:     "GET",
					URL:        "/api/resource",
					StatusCode: 200,
					Duration:   time.Duration(j) * time.Millisecond,
				})
			}
		}(i)
	}
	wg.Wait()

	snap, err := r.Finalize(RunMeta{StartTime: time.Now(), EndTime: time.Now()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snap.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(snap.Endpoints))
	}

	total := snap.Endpoints[0].RequestCount + r.Dropped()
	expected := int64(goroutines * entriesEach)
	if total != expected {
		t.Errorf("expected %d total (recorded+dropped), got %d", expected, total)
	}
}

func TestDefaultRecorder_DoubleFinalize(t *testing.T) {
	r := NewDefaultRecorder(16)
	meta := RunMeta{StartTime: time.Now(), EndTime: time.Now()}

	if _, err := r.Finalize(meta); err != nil {
		t.Fatalf("first Finalize failed: %v", err)
	}
	if _, err := r.Finalize(meta); err == nil {
		t.Error("expected error on second Finalize, got nil")
	}
}

func TestDefaultRecorder_BodySamples(t *testing.T) {
	r := NewDefaultRecorder(64)
	body := []byte(`{"id":"1","email":"test@example.com"}`)
	r.Record(RecordEntry{
		Method:     "GET",
		URL:        "/api/users",
		StatusCode: 200,
		Duration:   8 * time.Millisecond,
		RespBody:   body,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
	})

	// Finalize to flush the drain goroutine.
	if _, err := r.Finalize(RunMeta{StartTime: time.Now(), EndTime: time.Now()}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	samples := r.BodySamples("GET:/api/users")
	if len(samples) != 1 {
		t.Fatalf("expected 1 body sample, got %d", len(samples))
	}
	if string(samples[0]) != string(body) {
		t.Errorf("body sample mismatch: %q", samples[0])
	}
}

func TestDefaultRecorder_ConfigHash(t *testing.T) {
	cfg := &config.Config{
		ConfigSection: config.Section{HTTPFile: "test.http"},
		Stages:        []config.Stage{{TargetRPS: 100}},
	}
	r := NewDefaultRecorder(16)
	snap, _ := r.Finalize(RunMeta{Config: cfg, StartTime: time.Now(), EndTime: time.Now()})

	if snap.Meta.ConfigHash == "" {
		t.Error("expected non-empty ConfigHash")
	}
	if len(snap.Meta.ConfigHash) < 8 {
		t.Errorf("ConfigHash looks too short: %q", snap.Meta.ConfigHash)
	}
}

func TestPercentile(t *testing.T) {
	data := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	tests := []struct {
		p    float64
		want float64
	}{
		{0, 1},
		{50, 5.5},
		{100, 10},
	}
	for _, tc := range tests {
		got := percentile(data, tc.p)
		if got != tc.want {
			t.Errorf("percentile(%v, %v) = %v, want %v", data, tc.p, got, tc.want)
		}
	}
}
