package engine

import (
	"context"
	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
	"io"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

const userAgent = "gg/1.0"

type Metrics struct {
	totalRequests atomic.Int64
	successCount  atomic.Int64
	failureCount  atomic.Int64
	totalLatency  atomic.Int64
}

// rpsWindow is a fixed-size ring of per-second request counts used to
// compute a smooth, responsive current-RPS without any cumulative lag.
const rpsWindowSize = 3 // seconds to average over — short enough to be responsive

type rpsWindow struct {
	mu      sync.Mutex
	buckets [rpsWindowSize]int64
	seconds [rpsWindowSize]int64 // unix second each bucket belongs to
}

func (w *rpsWindow) record(count int64) {
	now := time.Now().Unix()
	w.mu.Lock()
	defer w.mu.Unlock()
	slot := int(now % rpsWindowSize)
	if w.seconds[slot] != now {
		w.seconds[slot] = now
		w.buckets[slot] = 0
	}
	w.buckets[slot] += count
}

// rate returns the request rate over the past rpsWindowSize seconds.
// It always divides by the full window width so there is no oscillation at
// second boundaries — only complete past seconds count (current second is
// excluded because it is still accumulating).
func (w *rpsWindow) rate() float64 {
	now := time.Now().Unix()
	w.mu.Lock()
	defer w.mu.Unlock()

	var total int64
	// Sum the (rpsWindowSize - 1) fully-completed seconds before now.
	// Skipping "now" avoids a partial-second low reading at the boundary.
	for i := 0; i < rpsWindowSize; i++ {
		age := now - w.seconds[i]
		if age >= 1 && age < rpsWindowSize {
			total += w.buckets[i]
		}
	}
	// Divide by the number of complete seconds we looked at.
	windowSecs := float64(rpsWindowSize - 1)
	if windowSecs < 1 {
		windowSecs = 1
	}
	return float64(total) / windowSecs
}

type MetricsSnapshot struct {
	TotalRequests int64
	SuccessCount  int64
	FailureCount  int64
	AvgLatency    float64
	MinLatency    float64
	MaxLatency    float64
	P50Latency    float64
	P95Latency    float64
	P99Latency    float64
	CurrentVPUs   int
	ActiveVPUs    int
	Throughput    float64
	ErrorRate     float64
	TargetRPS     int
	// Stage progress — updated by RunStages
	CurrentStage int
	TotalStages  int
	// Director Mode
	Bias int
}

type CallLog struct {
	Timestamp  time.Time
	Method     string
	Url        string
	StatusCode int
	Duration   time.Duration
	Error      string
}

type Engine struct {
	client     *http.Client
	metrics    *Metrics
	isRunning  atomic.Bool
	callLogs   []*CallLog
	errorLogs  []*CallLog
	callLogsMu sync.RWMutex
	maxLogs    int
	startTime  time.Time
	endTime    time.Time

	// targetRPS is written by the stage-runner and read by GetMetrics – use atomic.
	targetRPS atomic.Int64
	activeVPU atomic.Int32
	latencies []float64
	latencyMu sync.RWMutex

	// rpsWin gives a responsive current-rate without cumulative lag.
	rpsWin rpsWindow

	// stage progress (written by stage-runner goroutine, read by TUI)
	currentStage atomic.Int32
	totalStages  atomic.Int32

	// rpsBias is the cumulative manual RPS adjustment set via Director Mode.
	rpsBias atomic.Int64
	// biasCh receives delta values from the TUI; buffered so sends never block.
	biasCh chan int

	// recorder is the optional snap.Recorder. nil when --snap is not passed.
	// The hot-path is a single nil-check — zero overhead when snapping is off.
	recorder snap.Recorder

	// sampleCount is incremented on every request; used to gate body reads.
	sampleCount atomic.Int64
	// sampleEvery controls how often a response body is captured for schema
	// inference. 20 = 1-in-20 = 5 % (default). Set via WithSampleRate.
	sampleEvery int
}

// EngineOption is a functional option for New().
type EngineOption func(*Engine)

// WithRecorder attaches a snap.Recorder to the engine.
// When set, every HTTP response is passed to recorder.Record() after the body
// is drained. When nil (the default), the hot-path incurs zero overhead.
func WithRecorder(r snap.Recorder) EngineOption {
	return func(e *Engine) { e.recorder = r }
}

// WithSampleRate sets the fraction of responses whose body is captured for
// schema inference (0.0–1.0). Default is 0.05 (5 %).
// The rate is rounded to the nearest 1-in-N integer slot, so 0.05 → 1-in-20.
func WithSampleRate(rate float64) EngineOption {
	return func(e *Engine) {
		if rate <= 0 {
			e.sampleEvery = 0 // disable body capture
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

func New(opts ...EngineOption) *Engine {
	e := &Engine{
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 100,
				MaxIdleConns:        1000,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		metrics:     &Metrics{},
		callLogs:    make([]*CallLog, 0, 100),
		errorLogs:   make([]*CallLog, 0, 100),
		maxLogs:     100,
		biasCh:      make(chan int, 16),
		sampleEvery: 20, // 5 % default
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// SetTargetRPS allows live override (Director Mode – Phase 3).
func (e *Engine) SetTargetRPS(rps int) {
	e.targetRPS.Store(int64(rps))
}

// ApplyBias sends a RPS delta to the stage runner (e.g. +5 or -5).
// Non-blocking — drops silently if the channel is full (never happens with buffer=16).
func (e *Engine) ApplyBias(delta int) {
	select {
	case e.biasCh <- delta:
	default:
	}
}

// GetBias returns the current cumulative manual RPS bias.
func (e *Engine) GetBias() int {
	return int(e.rpsBias.Load())
}

// RunStages runs all configured stages sequentially, updating the ticker
// between stages so the engine follows the full plan.
func (e *Engine) RunStages(ctx context.Context, cfg *config.Config, specs []httpreader.RequestSpec) error {
	if len(specs) == 0 {
		return ErrNoRequests
	}
	if len(cfg.Stages) == 0 {
		return ErrNoStages
	}

	timeScale := cfg.ConfigSection.TimeScale
	if timeScale <= 0 {
		timeScale = 1.0
	}
	jitter := cfg.ConfigSection.Jitter // 0 = no jitter

	e.isRunning.Store(true)
	e.startTime = time.Now()
	e.latencyMu.Lock()
	e.latencies = make([]float64, 0, 1024)
	e.latencyMu.Unlock()
	e.totalStages.Store(int32(len(cfg.Stages)))
	defer func() {
		e.endTime = time.Now()
		e.isRunning.Store(false)
	}()

	// ── worker pool ───────────────────────────────────────────────────────
	// We size the pool generously so it can handle the peak RPS across all stages.
	peakRPS := cfg.PeakRPS()
	if peakRPS < 1 {
		peakRPS = 1
	}

	g, gCtx := errgroup.WithContext(ctx)
	work := make(chan httpreader.RequestSpec, peakRPS*2)

	// Spawn workers equal to peak RPS so the pool never starves.
	for i := 0; i < peakRPS; i++ {
		g.Go(func() error {
			for spec := range work {
				e.activeVPU.Add(1)
				if err := e.executeRequest(gCtx, spec); err != nil {
					e.metrics.failureCount.Add(1)
				} else {
					e.metrics.successCount.Add(1)
				}
				e.metrics.totalRequests.Add(1)
				e.rpsWin.record(1)
				e.activeVPU.Add(-1)
			}
			return nil
		})
	}

	// ── stage runner ──────────────────────────────────────────────────────
	// Runs in its own goroutine so it can block on each stage duration while
	// the workers above consume from the shared channel.
	g.Go(func() error {
		defer close(work) // signals all workers to exit when stages are done

		idx := 0     // round-robin across specs
		prevRPS := 0 // RPS at end of the previous stage (starts at 0)

		for stageIdx, stage := range cfg.Stages {
			// Zero-duration stage — instant step jump, no interpolation.
			if stage.Duration == 0 {
				prevRPS = stage.TargetRPS
				e.targetRPS.Store(int64(stage.TargetRPS))
				e.currentStage.Store(int32(stageIdx))
				continue
			}

			scaledDur := time.Duration(float64(stage.Duration) / timeScale)
			e.currentStage.Store(int32(stageIdx))

			stageStart := time.Now()
			stageEnd := stageStart.Add(scaledDur)

			startRPS := float64(prevRPS)
			endRPS := float64(stage.TargetRPS)

			nextFire := stageStart

			for {
				if gCtx.Err() != nil {
					return nil
				}

				now := time.Now()
				if now.After(stageEnd) {
					break
				}

				// ── Drain any pending bias deltas (non-blocking) ──────────
			drainBias:
				for {
					select {
					case delta := <-e.biasCh:
						e.rpsBias.Add(int64(delta))
					default:
						break drainBias
					}
				}

				// ── LERP: compute current target RPS ──────────────────────
				elapsed := now.Sub(stageStart)
				pct := float64(elapsed) / float64(scaledDur)
				if pct > 1 {
					pct = 1
				}
				currentRPS := startRPS + (endRPS-startRPS)*pct

				// Apply cumulative bias; clamp to minimum 1.
				biasedRPS := currentRPS + float64(e.rpsBias.Load())
				if biasedRPS < 1 {
					biasedRPS = 1
				}

				// Always publish the live interpolated+biased target to metrics.
				e.targetRPS.Store(int64(math.Round(biasedRPS)))

				// ── Drift-free ticker ─────────────────────────────────────
				baseInterval := time.Duration(float64(time.Second) / biasedRPS)
				if baseInterval < time.Millisecond {
					baseInterval = time.Millisecond
				}

				// Apply jitter symmetrically so the average rate is unchanged.
				jitterOffset := time.Duration(0)
				if jitter > 0 {
					jitterOffset = time.Duration(float64(baseInterval) * 2 * jitter * (rand.Float64() - 0.5))
				}

				nextFire = nextFire.Add(baseInterval + jitterOffset)

				// Sleep until the next fire time; wake early if bias arrives.
				sleepFor := time.Until(nextFire)
				if sleepFor > 0 {
					timer := time.NewTimer(sleepFor)
					select {
					case <-gCtx.Done():
						timer.Stop()
						return nil
					case delta := <-e.biasCh:
						// Bias changed mid-sleep — absorb, reset fire clock, re-LERP.
						timer.Stop()
						e.rpsBias.Add(int64(delta))
						nextFire = time.Now()
						continue
					case <-timer.C:
					}
				}

				// Emit work (non-blocking — drop if workers are saturated).
				select {
				case work <- specs[idx%len(specs)]:
					idx++
				default:
				}
			}

			prevRPS = stage.TargetRPS
		}
		return nil
	})

	return g.Wait()
}

// Run is kept for backward-compatibility (single stage).
func (e *Engine) Run(ctx context.Context, targetRPS int, duration time.Duration, specs []httpreader.RequestSpec) error {
	stageCfg := &config.Config{
		Stages: []config.Stage{
			{Duration: duration, TargetRPS: targetRPS},
		},
	}
	return e.RunStages(ctx, stageCfg, specs)
}

func (e *Engine) executeRequest(ctx context.Context, spec httpreader.RequestSpec) error {
	start := time.Now()
	defer func() {
		ms := float64(time.Since(start).Milliseconds())
		e.metrics.totalLatency.Add(int64(ms))
		e.latencyMu.Lock()
		e.latencies = append(e.latencies, ms)
		e.latencyMu.Unlock()
	}()

	req, err := spec.ToHTTPRequest(nil)
	if err != nil {
		duration := time.Since(start)
		e.logCall(spec.Method, spec.URL, 0, duration, err)
		if e.recorder != nil {
			e.recorder.Record(snap.RecordEntry{
				Timestamp: start,
				Method:    spec.Method,
				URL:       spec.URL,
				Duration:  duration,
				Error:     err,
			})
		}
		return err
	}
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", userAgent)

	resp, err := e.client.Do(req)
	duration := time.Since(start)

	if err != nil {
		e.logCall(spec.Method, spec.URL, 0, duration, err)
		if e.recorder != nil {
			e.recorder.Record(snap.RecordEntry{
				Timestamp: start,
				Method:    spec.Method,
				URL:       spec.URL,
				Duration:  duration,
				Error:     err,
			})
		}
		return err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	// Read or discard the body.
	// When a recorder is active and this request is within the sample window,
	// the body is captured for schema inference; otherwise it is discarded so
	// the TCP connection can be returned to the pool.
	var respBody []byte
	if e.recorder != nil && e.shouldSample() {
		respBody, _ = io.ReadAll(resp.Body) // fully consumes — connection reusable
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}

	var callErr error
	if resp.StatusCode >= 400 {
		callErr = ErrHttpError
	}
	e.logCall(spec.Method, spec.URL, resp.StatusCode, duration, callErr)

	if e.recorder != nil {
		e.recorder.Record(snap.RecordEntry{
			Timestamp:  start,
			Method:     spec.Method,
			URL:        spec.URL,
			StatusCode: resp.StatusCode,
			Duration:   duration,
			Headers:    resp.Header,
			RespBody:   respBody,
			Error:      callErr,
		})
	}

	if resp.StatusCode >= 400 {
		return ErrHttpError
	}
	return nil
}

func (e *Engine) computeLatency() (min, max, p50, p95, p99 float64) {
	e.latencyMu.RLock()
	if len(e.latencies) == 0 {
		e.latencyMu.RUnlock()
		return
	}
	data := make([]float64, len(e.latencies))
	copy(data, e.latencies)
	e.latencyMu.RUnlock()

	sort.Float64s(data)
	n := len(data)
	min = data[0]
	max = data[n-1]
	p50 = percentile(data, 50)
	p95 = percentile(data, 95)
	p99 = percentile(data, 99)
	return
}

func percentile(data []float64, p float64) float64 {
	if len(data) == 0 {
		return 0
	}
	idx := (p / 100) * float64(len(data)-1)
	lower := int(idx)
	upper := lower + 1
	if upper >= len(data) {
		return data[lower]
	}
	frac := idx - float64(lower)
	return data[lower] + frac*(data[upper]-data[lower])
}

func (e *Engine) GetMetrics() *MetricsSnapshot {
	total := e.metrics.totalRequests.Load()
	success := e.metrics.successCount.Load()
	failed := e.metrics.failureCount.Load()

	var avgLatency float64
	if total > 0 {
		avgLatency = float64(e.metrics.totalLatency.Load()) / float64(total)
	}

	var errorRate float64
	if total > 0 {
		errorRate = float64(failed) / float64(total)
	}

	// Use the sliding-window rate for a responsive current-RPS display.
	// Falls back to 0 when the engine hasn't started yet.
	throughput := e.rpsWin.rate()

	minL, maxL, p50, p95, p99 := e.computeLatency()

	return &MetricsSnapshot{
		TotalRequests: total,
		SuccessCount:  success,
		FailureCount:  failed,
		AvgLatency:    avgLatency,
		ErrorRate:     errorRate,
		Throughput:    throughput,
		ActiveVPUs:    int(e.activeVPU.Load()),
		TargetRPS:     int(e.targetRPS.Load()),
		MinLatency:    minL,
		MaxLatency:    maxL,
		P50Latency:    p50,
		P95Latency:    p95,
		P99Latency:    p99,
		CurrentVPUs:   0,
		CurrentStage:  int(e.currentStage.Load()),
		TotalStages:   int(e.totalStages.Load()),
		Bias:          int(e.rpsBias.Load()),
	}
}

func (e *Engine) IsRunning() bool {
	return e.isRunning.Load()
}

func (e *Engine) logCall(method, url string, statusCode int, duration time.Duration, err error) {
	entry := &CallLog{
		Timestamp:  time.Now(),
		Method:     method,
		Url:        url,
		StatusCode: statusCode,
		Duration:   duration,
	}
	if err != nil {
		entry.Error = err.Error()
	}

	e.callLogsMu.Lock()
	defer e.callLogsMu.Unlock()

	e.callLogs = append(e.callLogs, entry)
	if len(e.callLogs) > e.maxLogs {
		e.callLogs = e.callLogs[len(e.callLogs)-e.maxLogs:]
	}

	if entry.Error != "" || entry.StatusCode >= 400 {
		e.errorLogs = append(e.errorLogs, entry)
		if len(e.errorLogs) > e.maxLogs {
			e.errorLogs = e.errorLogs[len(e.errorLogs)-e.maxLogs:]
		}
	}
}

func (e *Engine) getRecentFromBuffer(buffer []*CallLog, count int) []CallLog {
	if count > len(buffer) {
		count = len(buffer)
	}
	if count == 0 {
		return []CallLog{}
	}
	src := buffer[len(buffer)-count:]
	logs := make([]CallLog, count)
	for i, p := range src {
		logs[i] = *p
	}
	return logs
}

func (e *Engine) GetRecentLogs(count int) []CallLog {
	e.callLogsMu.RLock()
	defer e.callLogsMu.RUnlock()
	return e.getRecentFromBuffer(e.callLogs, count)
}

func (e *Engine) GetRecentErrorLogs(count int) []CallLog {
	e.callLogsMu.RLock()
	defer e.callLogsMu.RUnlock()
	return e.getRecentFromBuffer(e.errorLogs, count)
}

func (e *Engine) GetElapsedTime() float64 {
	if e.startTime.IsZero() {
		return 0
	}
	if e.endTime.IsZero() {
		return time.Since(e.startTime).Seconds()
	}
	return e.endTime.Sub(e.startTime).Seconds()
}

// shouldSample returns true for 1-in-sampleEvery requests.
// sampleEvery == 0 means body capture is disabled.
func (e *Engine) shouldSample() bool {
	if e.sampleEvery == 0 {
		return false
	}
	return e.sampleCount.Add(1)%int64(e.sampleEvery) == 0
}
