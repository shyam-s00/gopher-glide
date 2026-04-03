package snap

import (
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
)

// ── WithMaxBodySamples / WithMaxBodyBytes options ─────────────────────────────

func TestWithMaxBodySamples_Applied(t *testing.T) {
	r := NewDefaultRecorder(64, WithMaxBodySamples(5))
	if r.maxBodySamples != 5 {
		t.Errorf("maxBodySamples: want 5, got %d", r.maxBodySamples)
	}
	_, _ = r.Finalize(RunMeta{StartTime: time.Now(), EndTime: time.Now()})
}

func TestWithMaxBodySamples_Zero_UsesDefault(t *testing.T) {
	r := NewDefaultRecorder(64, WithMaxBodySamples(0))
	acc := r.newAcc()
	if acc.maxBodySamples != DefaultMaxBodySamples {
		t.Errorf("expected DefaultMaxBodySamples (%d), got %d", DefaultMaxBodySamples, acc.maxBodySamples)
	}
	_, _ = r.Finalize(RunMeta{StartTime: time.Now(), EndTime: time.Now()})
}

func TestWithMaxBodyBytes_Applied(t *testing.T) {
	r := NewDefaultRecorder(64, WithMaxBodyBytes(1024))
	if r.maxBodyBytes != 1024 {
		t.Errorf("maxBodyBytes: want 1024, got %d", r.maxBodyBytes)
	}
	_, _ = r.Finalize(RunMeta{StartTime: time.Now(), EndTime: time.Now()})
}

// ── Record after close ────────────────────────────────────────────────────────

func TestRecord_AfterFinalize_IsDropped(t *testing.T) {
	r := NewDefaultRecorder(64)
	_, err := r.Finalize(RunMeta{StartTime: time.Now(), EndTime: time.Now()})
	if err != nil {
		t.Fatalf("first Finalize: %v", err)
	}
	// Recording after close must not panic.
	r.Record(RecordEntry{Method: "GET", URL: "/dropped", StatusCode: 200})
}

// ── resolveMaxSamples ─────────────────────────────────────────────────────────

func TestResolveMaxSamples_Zero(t *testing.T) {
	if got := resolveMaxSamples(0); got != DefaultMaxBodySamples {
		t.Errorf("resolveMaxSamples(0) = %d, want %d", got, DefaultMaxBodySamples)
	}
}

func TestResolveMaxSamples_Negative(t *testing.T) {
	if got := resolveMaxSamples(-5); got != DefaultMaxBodySamples {
		t.Errorf("resolveMaxSamples(-5) = %d, want %d", got, DefaultMaxBodySamples)
	}
}

func TestResolveMaxSamples_Positive(t *testing.T) {
	if got := resolveMaxSamples(50); got != 50 {
		t.Errorf("resolveMaxSamples(50) = %d, want 50", got)
	}
}

// ── recordBody reservoir mechanics ───────────────────────────────────────────

func TestRecordBody_FillsReservoir(t *testing.T) {
	acc := &endpointAcc{
		statusCodes:    make(map[int]int64),
		latenciesMs:    make([]float64, 0),
		bodySamples:    make([][]byte, 0, 3),
		maxBodySamples: 3,
	}
	for i := 0; i < 3; i++ {
		acc.recordBody([]byte("body"))
	}
	if len(acc.bodySamples) != 3 {
		t.Errorf("reservoir should hold 3 samples, got %d", len(acc.bodySamples))
	}
	if acc.bodyCount != 3 {
		t.Errorf("bodyCount should be 3, got %d", acc.bodyCount)
	}
}

func TestRecordBody_ReservoirDoesNotExceedCap(t *testing.T) {
	acc := &endpointAcc{
		statusCodes:    make(map[int]int64),
		latenciesMs:    make([]float64, 0),
		bodySamples:    make([][]byte, 0, 2),
		maxBodySamples: 2,
	}
	for i := 0; i < 20; i++ {
		acc.recordBody([]byte("data"))
	}
	if len(acc.bodySamples) > 2 {
		t.Errorf("reservoir must not exceed maxBodySamples=2, got %d", len(acc.bodySamples))
	}
	if acc.bodyCount != 20 {
		t.Errorf("bodyCount should track all 20 observed bodies, got %d", acc.bodyCount)
	}
}

func TestRecordBody_ByteBudgetFreezesReservoir(t *testing.T) {
	// The byte-budget guard is checked BEFORE a new body is appended,
	// so bodyBytesStored can overshoot the budget by at most one body's worth.
	// After the overshoot the reservoir is frozen: no more bodies accepted.
	const budget int64 = 10
	const bodySize = 6 // bytes per body
	acc := &endpointAcc{
		statusCodes:    make(map[int]int64),
		latenciesMs:    make([]float64, 0),
		bodySamples:    make([][]byte, 0, 100),
		maxBodySamples: 100,
		maxBodyBytes:   budget,
	}
	body := []byte("123456") // 6 bytes each
	for i := 0; i < 10; i++ {
		acc.recordBody(body)
	}
	// At most budget + bodySize bytes may be stored (soft cap).
	if acc.bodyBytesStored > budget+bodySize {
		t.Errorf("bodyBytesStored (%d) exceeded soft cap of budget+bodySize (%d)", acc.bodyBytesStored, budget+bodySize)
	}
	// Once frozen, adding more bodies must not increase bodyBytesStored.
	stored := acc.bodyBytesStored
	acc.recordBody(body)
	if acc.bodyBytesStored != stored {
		t.Errorf("reservoir should be frozen: stored was %d, now %d", stored, acc.bodyBytesStored)
	}
}

// ── configHash ────────────────────────────────────────────────────────────────

func TestConfigHash_NilReturnsEmpty(t *testing.T) {
	if got := configHash(nil); got != "" {
		t.Errorf("configHash(nil) = %q, want empty string", got)
	}
}

func TestConfigHash_ReturnsSha256Prefix(t *testing.T) {
	cfg := &config.Config{
		ConfigSection: config.Section{HTTPFile: "test.http"},
	}
	got := configHash(cfg)
	if !strings.HasPrefix(got, "sha256:") {
		t.Errorf("configHash should start with 'sha256:', got %q", got)
	}
}

func TestConfigHash_DifferentConfigsProduceDifferentHashes(t *testing.T) {
	cfg1 := &config.Config{ConfigSection: config.Section{HTTPFile: "a.http"}}
	cfg2 := &config.Config{ConfigSection: config.Section{HTTPFile: "b.http"}}
	h1 := configHash(cfg1)
	h2 := configHash(cfg2)
	if h1 == h2 {
		t.Error("different configs should produce different hashes")
	}
}

// ── BodySamples edge-cases ────────────────────────────────────────────────────

func TestBodySamples_UnknownEndpoint(t *testing.T) {
	r := NewDefaultRecorder(64)
	if _, err := r.Finalize(RunMeta{StartTime: time.Now(), EndTime: time.Now()}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	samples := r.BodySamples("GET:/nonexistent")
	if samples != nil {
		t.Errorf("expected nil for unknown endpoint, got %v", samples)
	}
}

func TestBodySamples_EndpointWithNoBody(t *testing.T) {
	r := NewDefaultRecorder(64)
	r.Record(RecordEntry{
		Method:     "GET",
		URL:        "/api/no-body",
		StatusCode: 204,
		Duration:   5 * time.Millisecond,
		// RespBody intentionally nil
	})
	if _, err := r.Finalize(RunMeta{StartTime: time.Now(), EndTime: time.Now()}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	samples := r.BodySamples("GET:/api/no-body")
	if samples != nil {
		t.Errorf("expected nil for endpoint with no body samples, got %v", samples)
	}
}

// ── percentile edge-cases ─────────────────────────────────────────────────────

func TestPercentile_EmptySlice(t *testing.T) {
	if got := percentile([]float64{}, 50); got != 0 {
		t.Errorf("percentile of empty slice should be 0, got %f", got)
	}
}

func TestPercentile_SingleElement(t *testing.T) {
	if got := percentile([]float64{42.0}, 99); got != 42.0 {
		t.Errorf("percentile of single element should be 42.0, got %f", got)
	}
}

// ── Finalize: snap settings are recorded ─────────────────────────────────────

func TestFinalize_SnapSettingsRecorded(t *testing.T) {
	r := NewDefaultRecorder(64, WithMaxBodySamples(10))
	snap, err := r.Finalize(RunMeta{
		SampleRate: 0.1,
		MaxSamples: 10,
		MaxBodyKB:  512,
		StartTime:  time.Now(),
		EndTime:    time.Now(),
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if snap.Meta.SnapSettings.SampleRate != 0.1 {
		t.Errorf("SampleRate: want 0.1, got %f", snap.Meta.SnapSettings.SampleRate)
	}
	if snap.Meta.SnapSettings.MaxSamples != 10 {
		t.Errorf("MaxSamples: want 10, got %d", snap.Meta.SnapSettings.MaxSamples)
	}
	if snap.Meta.SnapSettings.MaxBodyKB != 512 {
		t.Errorf("MaxBodyKB: want 512, got %d", snap.Meta.SnapSettings.MaxBodyKB)
	}
}

// ── Dropped counter ───────────────────────────────────────────────────────────

func TestDropped_NoneWhenBufferSufficient(t *testing.T) {
	r := NewDefaultRecorder(256)
	for i := 0; i < 10; i++ {
		r.Record(RecordEntry{Method: "GET", URL: "/api/x", StatusCode: 200, Duration: time.Millisecond})
	}
	if _, err := r.Finalize(RunMeta{StartTime: time.Now(), EndTime: time.Now()}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if d := r.Dropped(); d != 0 {
		t.Errorf("Dropped() = %d, want 0 (buffer was large enough)", d)
	}
}
