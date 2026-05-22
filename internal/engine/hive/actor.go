package hive

import (
	"context"
	"io"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// executeActor performs a single HTTP request on behalf of one Actor goroutine.
//
// It is the goroutine body for every Actor spawned by the Hatchery.
// Each invocation is independent — there is no shared mutable state beyond
// the thread-safe fields of Engine.
//
// Responsibilities in order:
//  1. Increment sharded totalRequests counter and rpsWindow.
//  2. Build the *http.Request via spec.ToHTTPRequest.
//  3. Attach context + User-Agent header.
//  4. Execute via the shared http.Client.
//  5. Drain or capture the response body (sampling gate).
//  6. Record latency into the sharded counter and the percentile slice.
//  7. Log the call (success or failure) into the ring buffer.
//  8. Increment sharded success/failure counter.
//  9. Forward the entry to snap.Recorder when configured.
//  10. Return ErrHttpError for any HTTP 4xx/5xx; transport errors propagate as-is.
//
// shard is the counter shard index passed by the Hatchery
// (actorIndex % numShards) to distribute writes without rand overhead.
func (e *Engine) executeActor(ctx context.Context, spec httpreader.RequestSpec, shard int) error {
	start := time.Now()

	// ── 1. Record request count and RPS window tick ───────────────────────
	e.counters.incTotalRequests(shard)
	e.rpsWin.record(1)

	// ── 2. Build request ─────────────────────────────────────────────────
	req, err := spec.ToHTTPRequest(nil)
	if err != nil {
		duration := time.Since(start)
		e.counters.incFailure(shard)
		e.recordLatency(shard, duration)
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

	// ── 2. Attach context + User-Agent ───────────────────────────────────
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", userAgent)

	// ── 3. Execute ───────────────────────────────────────────────────────
	resp, err := e.client.Do(req)
	duration := time.Since(start)

	if err != nil {
		e.counters.incFailure(shard)
		e.recordLatency(shard, duration)
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
	defer func() { _ = resp.Body.Close() }()

	// ── 4. Drain or capture body ─────────────────────────────────────────
	// When a recorder is active and this request falls within the sample
	// window, the body is captured for schema inference. Otherwise it is
	// discarded so the TCP connection is returned to the pool immediately.
	var respBody []byte
	sampled := false
	if e.recorder != nil && e.shouldSample() {
		sampled = true
		respBody, _ = io.ReadAll(resp.Body)
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}

	// Determine body size: prefer Content-Length (zero-cost for all
	// requests); fall back to actual read length for sampled responses.
	bodySize := int64(-1)
	if resp.ContentLength >= 0 {
		bodySize = resp.ContentLength
	} else if sampled {
		bodySize = int64(len(respBody))
	}

	// ── 5. Record latency ─────────────────────────────────────────────────
	e.recordLatency(shard, duration)

	// ── 6. Log call ───────────────────────────────────────────────────────
	var callErr error
	if resp.StatusCode >= 400 {
		callErr = ErrHttpError
	}
	e.logCall(spec.Method, spec.URL, resp.StatusCode, duration, callErr)

	// ── 8. Increment success/failure counter ──────────────────────────────
	if resp.StatusCode >= 400 {
		e.counters.incFailure(shard)
	} else {
		e.counters.incSuccess(shard)
	}

	// ── 9. Snap record ────────────────────────────────────────────────────
	if e.recorder != nil {
		e.recorder.Record(snap.RecordEntry{
			Timestamp:  start,
			Method:     spec.Method,
			URL:        spec.URL,
			StatusCode: resp.StatusCode,
			Duration:   duration,
			Headers:    resp.Header,
			RespBody:   respBody,
			BodySize:   bodySize,
			Error:      callErr,
		})
	}

	// ── 10. Return error for 4xx/5xx ─────────────────────────────────────
	if resp.StatusCode >= 400 {
		return ErrHttpError
	}
	return nil
}

// recordLatency appends the duration to the percentile slice and adds the
// millisecond value to the sharded totalLatency counter.
//
// Sub-millisecond durations are rounded up to 1 ms for the sharded counter so
// that the counter is always non-zero after any real request. The float64 slice
// used for percentile computation retains the true (possibly zero) ms value.
func (e *Engine) recordLatency(shard int, d time.Duration) {
	ms := float64(d.Milliseconds())
	shardMs := int64(ms)
	if shardMs == 0 && d > 0 {
		shardMs = 1 // round sub-ms up so the counter is always non-zero
	}
	e.counters.addLatency(shard, shardMs)
	e.latencyMu.Lock()
	e.latencies = append(e.latencies, ms)
	e.latencyMu.Unlock()
}
