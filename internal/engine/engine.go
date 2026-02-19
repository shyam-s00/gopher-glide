package engine

import (
	"context"
	"io"
	"net/http"
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
	isRunning  atomic.Bool // this is used to track if the engine is running
	callLogs   []CallLog
	callLogsMu sync.RWMutex
	maxLogs    int
	startTime  time.Time
	endTime    time.Time
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
		metrics:  &Metrics{},
		callLogs: make([]CallLog, 0, 100),
		maxLogs:  100, // just keep the last 100 logs
	}
}

func (e *Engine) Run(ctx context.Context, targetVPU int, duration time.Duration, url string) error {
	e.isRunning.Store(true)
	e.startTime = time.Now()
	defer func() {
		e.endTime = time.Now()
		e.isRunning.Store(false)
	}()

	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	g, gCtx := errgroup.WithContext(ctx)
	worker := make(chan struct{}, targetVPU*2)

	ticker := time.NewTicker(time.Second / time.Duration(targetVPU))
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

	for i := 0; i < targetVPU; i++ {
		g.Go(func() error {
			for range worker {
				if err := e.executeRequest(gCtx, url); err != nil {
					e.metrics.failureCount.Add(1)
				} else {
					e.metrics.successCount.Add(1)
				}
				e.metrics.totalRequests.Add(1)
			}
			return nil
		})
	}

	return g.Wait()
}

func (e *Engine) executeRequest(ctx context.Context, url string) error {
	start := time.Now()
	defer func() {
		e.metrics.totalLatency.Add(time.Since(start).Milliseconds())
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

	return &MetricsSnapshot{
		TotalRequests: total,
		SuccessCount:  success,
		FailureCount:  failed,
		AvgLatency:    avgLatency,
		ErrorRate:     errorRate,
		Throughput:    throughput,
		//TODO: add more metrics as we move forward
		MinLatency: 0, MaxLatency: 0, P50Latency: 0, P95Latency: 0, P99Latency: 0, ActiveVPUs: 0, CurrentVPUs: 0,
	}
}

func (e *Engine) IsRunning() bool {
	return e.isRunning.Load()
}

func (e *Engine) logCall(method, url string, statusCode int, duration time.Duration, err error) {
	e.callLogsMu.Lock()
	defer e.callLogsMu.Unlock()

	log := CallLog{
		Timestamp:  time.Now(),
		Method:     method,
		Url:        url,
		StatusCode: statusCode,
		Duration:   duration,
	}

	if err != nil {
		log.Error = err.Error()
	}

	e.callLogs = append(e.callLogs, log)
	if len(e.callLogs) > e.maxLogs {
		e.callLogs = e.callLogs[len(e.callLogs)-e.maxLogs:]
	}
}

func (e *Engine) GetRecentLogs(count int) []CallLog {
	e.callLogsMu.RLock()
	defer e.callLogsMu.RUnlock()

	if count > len(e.callLogs) {
		count = len(e.callLogs)
	}

	if count == 0 {
		return []CallLog{}
	}

	start := len(e.callLogs) - count
	logs := make([]CallLog, count)
	copy(logs, e.callLogs[start:])

	return logs
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
