package engine

import (
	"context"
	"io"
	"net/http"
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

type Engine struct {
	client  *http.Client
	metrics *Metrics
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
		metrics: &Metrics{},
	}
}

func (e *Engine) Run(ctx context.Context, targetVPU int, duration time.Duration, url string) error {
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
		return err
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

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

	return &MetricsSnapshot{
		TotalRequests: total,
		SuccessCount:  success,
		FailureCount:  failed,
		AvgLatency:    avgLatency,
		ErrorRate:     errorRate,
		//TODO: add more metrics as we move forward
		MinLatency: 0, MaxLatency: 0, P50Latency: 0, P95Latency: 0, P99Latency: 0, ActiveVPUs: 0, CurrentVPUs: 0, Throughput: 0,
	}
}
