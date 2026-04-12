// Package snap provides behavioral snapshot capture for gg load test runs.
// It is activated via the --snap CLI flag; when absent, the engine hot-path
// incurs zero overhead (a single nil-check on the Recorder field).
package snap

import (
	"crypto/sha256"
	"fmt"
	"math"
	"math/rand/v2"
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
	// BodySize is the response body byte count.
	// Set from Content-Length when available; from len(RespBody) for sampled
	// responses when Content-Length is absent; 0 when unknown.
	BodySize int64
}

// RunMeta carries top-level context about the load test run, supplied at
// Finalize time once the engine has completed all stages.
type RunMeta struct {
	Tag        string         // value of --snap-tag flag
	Config     *config.Config // full run config (used for config hash)
	StartTime  time.Time
	EndTime    time.Time
	PeakRPS    int
	SampleRate float64 // effective body-sample rate used for this run
	MaxSamples int     // effective per-endpoint reservoir cap (0 = DefaultMaxBodySamples)
	MaxBodyKB  int     // effective per-endpoint byte budget in KB (0 = unlimited)
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
	Tag           string       `json:"tag"`
	StartTime     time.Time    `json:"start_time"`
	EndTime       time.Time    `json:"end_time"`
	PeakRPS       int          `json:"peak_rps"`
	TotalRequests int64        `json:"total_requests"`
	ConfigHash    string       `json:"config_hash"`
	SnapSettings  SnapSettings `json:"snap_settings"` // tuning values used to produce this snap
}

// SnapSettings records the effective tuning parameters used when producing
// a snapshot. Stored in meta so the file is self-describing and two snaps
// taken with different settings can be trivially distinguished.
type SnapSettings struct {
	SampleRate float64 `json:"sample_rate"` // fraction of responses body-sampled (e.g. 0.05)
	MaxSamples int     `json:"max_samples"` // per-endpoint reservoir cap used
	MaxBodyKB  int     `json:"max_body_kb"` // per-endpoint byte budget in KB; 0 = unlimited
}

// EndpointSnap captures the behavioral profile of a single endpoint.
type EndpointSnap struct {
	ID                  string             `json:"id"`          // "METHOD:/path"
	StatusDist          map[string]float64 `json:"status_dist"` // "200": 0.97, …
	Latency             LatencyStats       `json:"latency"`
	PayloadSize         PayloadSizeStats   `json:"payload_size"` // response body size statistics in bytes
	ErrorRate           float64            `json:"error_rate"`
	RequestCount        int64              `json:"request_count"`         // total HTTP requests observed for this endpoint
	BodySamplesObserved int64              `json:"body_samples_observed"` // bodies that entered the reservoir (sample_rate × requests)
	BodySamplesStored   int64              `json:"body_samples_stored"`   // bodies actually kept (≤ max_samples / byte budget)
	Schema              *SchemaSnapshot    `json:"schema,omitempty"`      // populated when stored bodies are valid JSON
}

// PayloadSizeStats holds response body size statistics in bytes.
// Populated from Content-Length headers (all requests) and actual body reads
// (sampled requests), so accuracy improves with higher sample rates.
type PayloadSizeStats struct {
	Avg float64 `json:"avg"` // mean body size across observed responses
	P95 float64 `json:"p95"` // 95th-percentile body size
	Max float64 `json:"max"` // largest body seen
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
	SampleCount int                    `json:"sample_count"` // number of body samples used for inference
	Fields      map[string]FieldSchema `json:"fields"`
}

// FieldSchema describes a single JSON field observed across response bodies.
type FieldSchema struct {
	Type      string  `json:"type"`
	Presence  float64 `json:"presence"`  // 0.0–1.0 fraction of samples containing the field
	Stability string  `json:"stability"` // STABLE / VOLATILE / RARE
}

// DefaultMaxBodySamples is the per-endpoint reservoir cap used when neither
// a CLI flag nor a config.yaml value overrides it. 200 samples gives presence
// fractions accurate to ±2 %, which is sufficient for stable schema inference.
const DefaultMaxBodySamples = 200

// endpointAcc accumulates raw observations for a single endpoint key.
// All methods are safe for concurrent use via its internal mutex.
type endpointAcc struct {
	mu          sync.Mutex
	statusCodes map[int]int64
	latenciesMs []float64
	errorCount  int64
	totalCount  int64

	// payloadSizes tracks the byte size of each response where size is known
	// (from Content-Length or from a sampled body read). Used to compute
	// PayloadSizeStats: avg, p95, and max.
	payloadSizes []float64

	// Body-sample reservoir (Knuth Algorithm R).
	bodySamples     [][]byte // fixed-capacity slice; len ≤ maxBodySamples
	bodyCount       int64    // total bodies seen (drives reservoir probability)
	bodyBytesStored int64    // bytes currently held across bodySamples

	// Limits set once at creation; never mutated afterwards.
	maxBodySamples int   // reservoir capacity, always > 0
	maxBodyBytes   int64 // per-endpoint byte budget; 0 = no byte-based limit
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

	// Track payload size whenever we have a known body size (> 0).
	if entry.BodySize > 0 {
		a.payloadSizes = append(a.payloadSizes, float64(entry.BodySize))
	}

	if len(entry.RespBody) > 0 {
		a.recordBody(entry.RespBody)
	}
}

// recordBody adds body to the reservoir using Knuth Algorithm R, subject to
// an optional byte budget. Must be called with a.mu held.
func (a *endpointAcc) recordBody(body []byte) {
	newLen := int64(len(body))

	// Byte-budget guard: freeze the reservoir once the budget is exhausted.
	if a.maxBodyBytes > 0 && a.bodyBytesStored >= a.maxBodyBytes {
		return
	}

	a.bodyCount++

	if a.bodyCount <= int64(a.maxBodySamples) {
		// Reservoir not yet full — append directly.
		cp := make([]byte, len(body))
		copy(cp, body)
		a.bodySamples = append(a.bodySamples, cp)
		a.bodyBytesStored += newLen
		return
	}

	// Reservoir full: replace a uniformly-random existing slot with probability
	// maxBodySamples/bodyCount. This keeps every body in the stream equally
	// likely to appear in the final set, regardless of run length.
	j := rand.Int64N(a.bodyCount)
	if j < int64(a.maxBodySamples) {
		oldLen := int64(len(a.bodySamples[j]))
		// Byte-budget check: ensure the swap itself doesn't bust the budget.
		if a.maxBodyBytes > 0 && (a.bodyBytesStored-oldLen+newLen) > a.maxBodyBytes {
			return
		}
		cp := make([]byte, len(body))
		copy(cp, body)
		a.bodyBytesStored = a.bodyBytesStored - oldLen + newLen
		a.bodySamples[j] = cp
	}
}

// toEndpointSnap computes aggregated statistics and returns an EndpointSnap.
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

	// Payload size statistics from all responses with a known body size.
	var payloadStats PayloadSizeStats
	if len(a.payloadSizes) > 0 {
		sorted := make([]float64, len(a.payloadSizes))
		copy(sorted, a.payloadSizes)
		sort.Float64s(sorted)
		var sum float64
		for _, s := range sorted {
			sum += s
		}
		payloadStats = PayloadSizeStats{
			Avg: sum / float64(len(sorted)),
			P95: percentile(sorted, 95),
			Max: sorted[len(sorted)-1],
		}
	}

	var errRate float64
	if a.totalCount > 0 {
		errRate = float64(a.errorCount) / float64(a.totalCount)
	}

	return EndpointSnap{
		ID:                  id,
		StatusDist:          statusDist,
		Latency:             stats,
		PayloadSize:         payloadStats,
		ErrorRate:           errRate,
		RequestCount:        a.totalCount,
		BodySamplesObserved: a.bodyCount,
		BodySamplesStored:   int64(len(a.bodySamples)),
	}
}

// DefaultRecorder is the production Recorder implementation.
// Entries are enqueued to a buffered channel and drained by a single background
// goroutine, keeping Record() non-blocking in the engine hot-path.
type DefaultRecorder struct {
	ch             chan RecordEntry
	wg             sync.WaitGroup
	endpoints      sync.Map // key: "METHOD:URL" → *endpointAcc
	dropped        atomic.Int64
	closed         atomic.Bool
	sanitizer      Sanitizer // applied in drain() before accumulation; never nil
	maxBodySamples int       // reservoir cap passed to new endpointAcc instances; 0 = use default
	maxBodyBytes   int64     // byte budget passed to new endpointAcc instances; 0 = no limit
}

// RecorderOption is a functional option for DefaultRecorder.
type RecorderOption func(*DefaultRecorder)

// WithSanitizer replaces the recorder's sanitizer.
// Pass NoopSanitizer{} to disable scrubbing entirely.
func WithSanitizer(s Sanitizer) RecorderOption {
	return func(r *DefaultRecorder) { r.sanitizer = s }
}

// WithExtraHeaders extends the default sensitive-header strip-list with the
// provided names. The built-in defaults (Authorization, Cookie, Set-Cookie,
// X-Api-Key) are always included.
//
//	NewDefaultRecorder(n, WithExtraHeaders("X-Internal-Token", "X-Debug"))
func WithExtraHeaders(headers ...string) RecorderOption {
	return func(r *DefaultRecorder) { r.sanitizer = NewSanitizerWithExtraHeaders(headers...) }
}

// WithMaxBodySamples sets the per-endpoint reservoir cap for body samples used
// in schema inference. Reservoir sampling (Knuth Algorithm R) ensures the
// stored set is statistically unbiased regardless of run length.
// Values ≤ 0 fall back to DefaultMaxBodySamples (200).
func WithMaxBodySamples(n int) RecorderOption {
	return func(r *DefaultRecorder) { r.maxBodySamples = n }
}

// WithMaxBodyBytes sets the per-endpoint byte budget for stored body samples.
// Once an endpoint's stored bytes reach this limit, no further bodies (or
// reservoir replacements) are accepted for that endpoint.
// 0 means no byte-based limit; the reservoir sample count is the primary guard.
func WithMaxBodyBytes(n int64) RecorderOption {
	return func(r *DefaultRecorder) { r.maxBodyBytes = n }
}

// NewDefaultRecorder creates a DefaultRecorder with a buffered channel of
// bufSize entries. A value of 0 or less defaults to 4096.
// By default a DefaultSanitizer is installed; override with WithSanitizer.
func NewDefaultRecorder(bufSize int, opts ...RecorderOption) *DefaultRecorder {
	if bufSize <= 0 {
		bufSize = 4096
	}
	r := &DefaultRecorder{
		ch:        make(chan RecordEntry, bufSize),
		sanitizer: NewDefaultSanitizer(),
	}
	for _, opt := range opts {
		opt(r)
	}
	r.wg.Add(1)
	go r.drain()
	return r
}

// newAcc creates a fresh endpointAcc pre-configured with this recorder's
// body-sample limits. Called from drain() via LoadOrStore.
func (r *DefaultRecorder) newAcc() *endpointAcc {
	maxSamples := r.maxBodySamples
	if maxSamples <= 0 {
		maxSamples = DefaultMaxBodySamples
	}
	return &endpointAcc{
		statusCodes:    make(map[int]int64),
		latenciesMs:    make([]float64, 0, 64),
		bodySamples:    make([][]byte, 0, maxSamples),
		maxBodySamples: maxSamples,
		maxBodyBytes:   r.maxBodyBytes,
	}
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

// drain is the single background goroutine that processes entries from ch.
// It sanitizes each entry before accumulation, then exits when the channel
// is closed by Finalize.
//
// The load-then-store pattern avoids calling newAcc() (which pre-allocates
// several slices including a [][]byte of cap=maxBodySamples) on every entry.
// In steady state, the endpoint key already exists and Load returns immediately
// with zero allocation. newAcc() is only invoked on a genuine first-seen key,
// and LoadOrStore is used for that store to guard against concurrent readers
// (e.g. BodySamples) that may be racing on the same key.
func (r *DefaultRecorder) drain() {
	defer r.wg.Done()
	for entry := range r.ch {
		entry = r.sanitizer.Sanitize(entry)
		key := entry.Method + ":" + entry.URL
		val, ok := r.endpoints.Load(key)
		if !ok {
			val, _ = r.endpoints.LoadOrStore(key, r.newAcc())
		}
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
			SnapSettings: SnapSettings{
				SampleRate: meta.SampleRate,
				MaxSamples: resolveMaxSamples(meta.MaxSamples),
				MaxBodyKB:  meta.MaxBodyKB,
			},
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
		totalRequests += ep.RequestCount
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

// resolveMaxSamples returns the effective max-samples value for recording in
// SnapSettings. When n is 0 the recorder uses DefaultMaxBodySamples.
func resolveMaxSamples(n int) int {
	if n <= 0 {
		return DefaultMaxBodySamples
	}
	return n
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
