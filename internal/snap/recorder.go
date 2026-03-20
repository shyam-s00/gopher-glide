// Package snap provides behavioral snapshot capture for gg load test runs.
// It is activated via the --snap CLI flag; when absent, the engine hot-path
// incurs zero overhead (a single nil-check on the Recorder field).
package snap

import (
	"crypto/sha256"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
)

// Recorder is the interface called from the engine hot-path after each HTTP
// response. Implementations must be safe for concurrent use.
type Recorder interface {
	// Record captures a single HTTP response entry.
	// It must be non-blocking (channel write or atomic accumulator only).
	Record(entry RecordEntry)

	// Finalize aggregates all in-memory samples into a Snapshot and returns it.
	// It is called once after RunStages completes.
	Finalize(meta RunMeta) (*Snapshot, error)
}

// RecordEntry holds the data captured for a single HTTP response.
type RecordEntry struct {
	Timestamp  time.Time
	Method     string
	URL        string
	StatusCode int
	Duration   time.Duration
	RespBody   []byte // populated only when the sample-rate trigger fires
	Headers    http.Header
	Error      error
}

// RunMeta carries top-level context about the load test run, supplied at
// Finalize time once the engine has completed all stages.
type RunMeta struct {
	Tag       string         // value of --snap-tag flag
	Config    *config.Config // full run config (used for config hash)
	StartTime time.Time
	EndTime   time.Time
	PeakRPS   int
}

// ── Snapshot types (written by format.go, read by diff.go, etc.) ─────────────

// Snapshot is the complete behavioral fingerprint of a single load test run.
type Snapshot struct {
	Version   int            `json:"version"`
	Meta      SnapMeta       `json:"meta"`
	Endpoints []EndpointSnap `json:"endpoints"`
}

// SnapMeta holds run-level metadata stored in the snapshot file.
type SnapMeta struct {
	Tag           string    `json:"tag"`
	StartTime     time.Time `json:"start_time"`
	EndTime       time.Time `json:"end_time"`
	PeakRPS       int       `json:"peak_rps"`
	TotalRequests int64     `json:"total_requests"`
	ConfigHash    string    `json:"config_hash"`
}

// EndpointSnap captures the behavioral profile of a single endpoint.
type EndpointSnap struct {
	ID          string             `json:"id"`          // "METHOD:/path"
	StatusDist  map[string]float64 `json:"status_dist"` // "200": 0.97, …
	Latency     LatencyStats       `json:"latency"`
	ErrorRate   float64            `json:"error_rate"`
	SampleCount int64              `json:"sample_count"`
	Schema      *SchemaSnapshot    `json:"schema,omitempty"` // populated by schema.go
}

// LatencyStats holds response-time percentiles in milliseconds.
type LatencyStats struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

// SchemaSnapshot is the inferred JSON response schema for an endpoint.
type SchemaSnapshot struct {
	Fields map[string]FieldSchema `json:"fields"`
}

// FieldSchema describes a single JSON field observed across response bodies.
type FieldSchema struct {
	Type      string  `json:"type"`
	Presence  float64 `json:"presence"`  // 0.0–1.0 fraction of samples containing the field
	Stability string  `json:"stability"` // STABLE / VOLATILE / RARE
}

// endpointAcc accumulates raw observations for a single endpoint key.
// All methods are safe for concurrent use via its internal mutex.
type endpointAcc struct {
	mu          sync.Mutex
	statusCodes map[int]int64
	latenciesMs []float64
	errorCount  int64
	totalCount  int64
	bodySamples [][]byte // retained for schema inference (schema.go)
}

func newEndpointAcc() *endpointAcc {
	return &endpointAcc{
		statusCodes: make(map[int]int64),
		latenciesMs: make([]float64, 0, 64),
	}
}

func (a *endpointAcc) record(entry RecordEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.totalCount++

	if entry.StatusCode > 0 {
		a.statusCodes[entry.StatusCode]++
	}
	a.latenciesMs = append(a.latenciesMs, float64(entry.Duration.Milliseconds()))

	if entry.Error != nil || entry.StatusCode >= 400 {
		a.errorCount++
	}

	// Retain a copy of sampled bodies for schema inference.
	if len(entry.RespBody) > 0 {
		body := make([]byte, len(entry.RespBody))
		copy(body, entry.RespBody)
		a.bodySamples = append(a.bodySamples, body)
	}
}

// toEndpointSnap computes aggregated statistics and returns an EndpointSnap.
// The Schema field is left nil; schema.go will populate it in Task 2.
func (a *endpointAcc) toEndpointSnap(id string) EndpointSnap {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Status distribution — normalized to [0, 1].
	statusDist := make(map[string]float64, len(a.statusCodes))
	if a.totalCount > 0 {
		for code, count := range a.statusCodes {
			statusDist[fmt.Sprintf("%d", code)] = float64(count) / float64(a.totalCount)
		}
	}

	// Latency percentiles over a sorted copy, so the original slice is untouched.
	var stats LatencyStats
	if len(a.latenciesMs) > 0 {
		sorted := make([]float64, len(a.latenciesMs))
		copy(sorted, a.latenciesMs)
		sort.Float64s(sorted)
		stats = LatencyStats{
			P50: percentile(sorted, 50),
			P95: percentile(sorted, 95),
			P99: percentile(sorted, 99),
			Max: sorted[len(sorted)-1],
		}
	}

	var errRate float64
	if a.totalCount > 0 {
		errRate = float64(a.errorCount) / float64(a.totalCount)
	}

	return EndpointSnap{
		ID:          id,
		StatusDist:  statusDist,
		Latency:     stats,
		ErrorRate:   errRate,
		SampleCount: a.totalCount,
	}
}

// DefaultRecorder is the production Recorder implementation.
// Entries are enqueued to a buffered channel and drained by a single background
// goroutine, keeping Record() non-blocking in the engine hot-path.
type DefaultRecorder struct {
	ch        chan RecordEntry
	wg        sync.WaitGroup
	endpoints sync.Map // key: "METHOD:URL" → *endpointAcc
	dropped   atomic.Int64
	closed    atomic.Bool
}

// NewDefaultRecorder creates a DefaultRecorder with a buffered channel of
// bufSize entries. A value of 0 or lesser defaults to 4096.
func NewDefaultRecorder(bufSize int) *DefaultRecorder {
	if bufSize <= 0 {
		bufSize = 4096
	}
	r := &DefaultRecorder{
		ch: make(chan RecordEntry, bufSize),
	}
	r.wg.Add(1)
	go r.drain()
	return r
}

// Record enqueues an entry for background processing.
// It never blocks — if the channel is full, the entry is silently dropped and
// the internal drop counter is incremented.
func (r *DefaultRecorder) Record(entry RecordEntry) {
	if r.closed.Load() {
		return
	}
	select {
	case r.ch <- entry:
	default:
		r.dropped.Add(1)
	}
}

// Drain is the single background goroutine that processes entries from ch.
// It exits when the channel is closed by Finalize.
func (r *DefaultRecorder) drain() {
	defer r.wg.Done()
	for entry := range r.ch {
		key := entry.Method + ":" + entry.URL
		val, _ := r.endpoints.LoadOrStore(key, newEndpointAcc())
		val.(*endpointAcc).record(entry)
	}
}

// Finalize closes the internal channel, waits for the drain goroutine to
// flush all queued entries, then assembles, and returns a Snapshot.
// It may only be called once; subsequent calls return an error.
func (r *DefaultRecorder) Finalize(meta RunMeta) (*Snapshot, error) {
	if r.closed.Swap(true) {
		return nil, fmt.Errorf("snap: recorder already finalized")
	}
	close(r.ch)
	r.wg.Wait() // drain goroutine has processed every queued entry

	snap := &Snapshot{
		Version: 1,
		Meta: SnapMeta{
			Tag:        meta.Tag,
			StartTime:  meta.StartTime,
			EndTime:    meta.EndTime,
			PeakRPS:    meta.PeakRPS,
			ConfigHash: configHash(meta.Config),
		},
	}

	var totalRequests int64
	r.endpoints.Range(func(key, val any) bool {
		id := key.(string)
		acc := val.(*endpointAcc)
		ep := acc.toEndpointSnap(id)
		// drain goroutine has exited (r.wg.Wait() above), so bodySamples is
		// immutable here — safe to read without the lock.
		if len(acc.bodySamples) > 0 {
			ep.Schema = InferSchema(acc.bodySamples)
		}
		snap.Endpoints = append(snap.Endpoints, ep)
		totalRequests += ep.SampleCount
		return true
	})
	snap.Meta.TotalRequests = totalRequests

	return snap, nil
}

// Dropped returns the number of RecordEntry values dropped due to a full
// channel. A non-zero count suggests the bufSize should be increased.
func (r *DefaultRecorder) Dropped() int64 {
	return r.dropped.Load()
}

// BodySamples returns the raw body samples collected for a given endpoint key
// ("METHOD:URL"). Returns nil if the key was never observed or had no bodies.
// This is used by schema.go to drive inference.
func (r *DefaultRecorder) BodySamples(endpointID string) [][]byte {
	val, ok := r.endpoints.Load(endpointID)
	if !ok {
		return nil
	}
	acc := val.(*endpointAcc)
	acc.mu.Lock()
	defer acc.mu.Unlock()
	if len(acc.bodySamples) == 0 {
		return nil
	}
	out := make([][]byte, len(acc.bodySamples))
	copy(out, acc.bodySamples)
	return out
}

// configHash returns a short SHA-256 fingerprint of the config struct for
// change-detection across runs.
func configHash(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	h := sha256.New()
	_, err := fmt.Fprintf(h, "%+v", cfg)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// percentile computes the p-th percentile of a pre-sorted float64 slice
// using linear interpolation (same algorithm as engine.go).
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p / 100) * float64(len(sorted)-1)
	lo := math.Floor(idx)
	hi := math.Ceil(idx)
	if int(hi) >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := idx - lo
	return sorted[int(lo)] + frac*(sorted[int(hi)]-sorted[int(lo)])
}
