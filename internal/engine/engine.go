package engine

import (
	"context"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

type Metrics struct {
	totalRequests atomic.Int64
	successCount  atomic.Int64
	failureCount  atomic.Int64
	totalLatency  atomic.Int64
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
	callLogs   []*CallLog // ring buffer of all requests
	errorLogs  []*CallLog // ring buffer of error requests — shares the same *CallLog pointers, no duplication
	callLogsMu sync.RWMutex
	maxLogs    int
	startTime  time.Time
	endTime    time.Time
	targetRPS  int
	activeVPU  atomic.Int32
	latencies  []float64
	latencyMu  sync.RWMutex
}

func New() *Engine {
	return &Engine{
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 100,
				MaxIdleConns:        1000,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		metrics:   &Metrics{},
		callLogs:  make([]*CallLog, 0, 100),
		errorLogs: make([]*CallLog, 0, 100),
		maxLogs:   100,
	}
}

func (e *Engine) Run(ctx context.Context, targetRPS int, duration time.Duration, url string) error {
	e.isRunning.Store(true)
	e.targetRPS = targetRPS
	e.startTime = time.Now()
	e.latencies = make([]float64, 0, 1024)
	defer func() {
		e.endTime = time.Now()
		e.isRunning.Store(false)
	}()

	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	g, gCtx := errgroup.WithContext(ctx)
	worker := make(chan struct{}, targetRPS*2)

	ticker := time.NewTicker(time.Second / time.Duration(targetRPS))
	defer ticker.Stop()

	//distributor
	g.Go(func() error {
		for {
			select {
			case <-gCtx.Done():
				close(worker)
				return nil
			case <-ticker.C:
				select {
				case worker <- struct{}{}:
				default:
					// is the channel full??
				}
			}
		}
	})

	//worker pool
	//workerPool := sync.Pool{
	//	New: func() interface{} {
	//		return &http.Client{}
	//	},
	//}

	for i := 0; i < targetRPS; i++ {
		g.Go(func() error {
			for range worker {
				e.activeVPU.Add(1)
				if err := e.executeRequest(gCtx, url); err != nil {
					e.metrics.failureCount.Add(1)
				} else {
					e.metrics.successCount.Add(1)
				}
				e.metrics.totalRequests.Add(1)
				e.activeVPU.Add(-1)
			}
			return nil
		})
	}

	return g.Wait()
}

func (e *Engine) executeRequest(ctx context.Context, url string) error {
	start := time.Now()
	defer func() {
		ms := float64(time.Since(start).Milliseconds())
		e.metrics.totalLatency.Add(int64(ms))
		e.latencyMu.Lock()
		e.latencies = append(e.latencies, ms)
		e.latencyMu.Unlock()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		duration := time.Since(start)
		e.logCall(http.MethodGet, url, 0, duration, err)
		e.metrics.totalLatency.Add(duration.Milliseconds())
		//e.metrics.totalRequests.Add(duration.Milliseconds())
		return err
	}

	resp, err := e.client.Do(req)
	duration := time.Since(start)
	e.metrics.totalLatency.Add(duration.Milliseconds())

	if err != nil {
		e.logCall(http.MethodGet, url, 0, duration, err)
		return err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	var callErr error
	if resp.StatusCode >= 400 {
		callErr = ErrHttpError
	}
	e.logCall(http.MethodGet, url, resp.StatusCode, duration, callErr)

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

	var throughput float64
	if !e.startTime.IsZero() { //started and startTime is set now
		var elapsed float64
		if e.endTime.IsZero() {
			elapsed = time.Since(e.startTime).Seconds() // we are still running
		} else {
			elapsed = e.endTime.Sub(e.startTime).Seconds() // stopped
		}

		if elapsed > 0 {
			throughput = float64(total) / elapsed
		}
	}

	minL, maxL, p50, p95, p99 := e.computeLatency()

	return &MetricsSnapshot{
		TotalRequests: total,
		SuccessCount:  success,
		FailureCount:  failed,
		AvgLatency:    avgLatency,
		ErrorRate:     errorRate,
		Throughput:    throughput,
		ActiveVPUs:    int(e.activeVPU.Load()),
		TargetRPS:     e.targetRPS,
		MinLatency:    minL,
		MaxLatency:    maxL,
		P50Latency:    p50,
		P95Latency:    p95,
		P99Latency:    p99,
		CurrentVPUs:   0,
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

	// append to the all-requests ring buffer
	e.callLogs = append(e.callLogs, entry)
	if len(e.callLogs) > e.maxLogs {
		e.callLogs = e.callLogs[len(e.callLogs)-e.maxLogs:]
	}

	// errors share the same pointer — zero extra allocation
	if entry.Error != "" || entry.StatusCode >= 400 {
		e.errorLogs = append(e.errorLogs, entry)
		if len(e.errorLogs) > e.maxLogs {
			e.errorLogs = e.errorLogs[len(e.errorLogs)-e.maxLogs:]
		}
	}
}

// getRecentFromBuffer returns the most recent `count` entries from buffer.
// Must be called with callLogsMu held (at least read-locked).
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
		logs[i] = *p // copy value out so the caller has no shared pointer
	}
	return logs
}

func (e *Engine) GetRecentLogs(count int) []CallLog {
	e.callLogsMu.RLock()
	defer e.callLogsMu.RUnlock()

	return e.getRecentFromBuffer(e.callLogs, count)
}

// GetRecentErrorLogs returns the most recent error-only entries from a dedicated
// ring buffer. Each entry is a pointer-shared copy of the same *CallLog already
// in callLogs — no duplicate heap allocation.
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
		// still running
		return time.Since(e.startTime).Seconds()
	}
	//stopped
	return e.endTime.Sub(e.startTime).Seconds()
}
