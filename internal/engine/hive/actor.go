package hive

import (
	"context"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// executeJourney runs every spec in the journey sequentially using a single
// shared ActorMemory. Variables extracted by a @gg-export directive on step N
// are automatically injected into the URL, headers, and body of step N+1…M.
//
// The journey aborts on the first failed step (HTTP 4xx/5xx, transport error,
// or variable-extraction failure); subsequent steps are not executed.
//
// shard is passed through to executeActor unchanged so that counter writes
// are distributed across shards without rand overhead.
func (e *Engine) executeJourney(ctx context.Context, specs []httpreader.RequestSpec, shard int) error {
	mem := newActorMemory()
	for _, spec := range specs {
		if err := e.executeActor(ctx, spec, shard, &mem); err != nil {
			return err
		}
	}
	return nil
}

// executeActor performs a single HTTP request step within a Journey.
//
// mem carries the Actor's private variable store across steps; it is nil only
// in stateless (single-request) callers. When non-nil:
//   - Variables already in mem are injected into the spec's URL, headers, and
//     body via RequestSpec.ToHTTPRequest before the request is built.
//   - After a 2xx response, every @gg-export directive attached to the spec is
//     evaluated against the response body and the result is stored in mem for
//     use by subsequent steps.
//
// Failure policy:
//   - Transport error         → incFailure, return the error.
//   - HTTP 4xx / 5xx          → incFailure, return ErrHttpError.
//   - @gg-export extract fail → incFailure, return ErrExtractionFailed.
//
// shard is the counter-shard index (actorIndex % numShards).
func (e *Engine) executeActor(ctx context.Context, spec httpreader.RequestSpec, shard int, mem *ActorMemory) error {
	start := time.Now()

	// ── 1. Record request count and RPS window tick ───────────────────────
	e.counters.incTotalRequests(shard)
	e.rpsWin.record(1)

	// ── 2. Build request (inject variables from actor memory) ────────────
	var vars map[string]string
	if mem != nil {
		vars = mem.ToMap()
	}
	req, err := spec.ToHTTPRequest(vars)
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

	// ── 3. Attach context + User-Agent ───────────────────────────────────
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", userAgent)

	resolvedURL := req.URL.String()

	// ── 4. Execute ───────────────────────────────────────────────────────
	resp, err := e.client.Do(req)
	duration := time.Since(start)

	if err != nil {
		e.counters.incFailure(shard)
		e.recordLatency(shard, duration)
		e.logCall(spec.Method, resolvedURL, 0, duration, err)
		if e.recorder != nil {
			e.recorder.Record(snap.RecordEntry{
				Timestamp: start,
				Method:    spec.Method,
				URL:       resolvedURL,
				Duration:  duration,
				Error:     err,
			})
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// ── 5. Drain or capture body ─────────────────────────────────────────
	// The body must be read (not discarded) when either:
	//   a) the snap recorder is active and this request is within the sample
	//      window (schema inference), or
	//   b) the spec has @gg-export directives (variable extraction).
	doSample := e.recorder != nil && e.shouldSample()
	needBody := doSample || (mem != nil && len(spec.Exports) > 0)

	var respBody []byte
	if needBody {
		respBody, _ = io.ReadAll(resp.Body)
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}

	// Prefer Content-Length for body-size reporting (zero extra cost);
	// fall back to actual byte count only for sampled responses.
	bodySize := int64(-1)
	if resp.ContentLength >= 0 {
		bodySize = resp.ContentLength
	} else if doSample {
		bodySize = int64(len(respBody))
	}

	// ── 6. Record latency ─────────────────────────────────────────────────
	e.recordLatency(shard, duration)

	// ── 7. HTTP error gate ────────────────────────────────────────────────
	// Log, count, snap, and abort — extraction is never attempted on a
	// failed HTTP response.
	if resp.StatusCode >= 400 {
		e.logCall(spec.Method, resolvedURL, resp.StatusCode, duration, ErrHttpError)
		e.counters.incFailure(shard)
		if e.recorder != nil {
			snapBody := respBody
			if !doSample {
				snapBody = nil
			}
			e.recorder.Record(snap.RecordEntry{
				Timestamp:  start,
				Method:     spec.Method,
				URL:        resolvedURL,
				StatusCode: resp.StatusCode,
				Duration:   duration,
				Headers:    resp.Header,
				RespBody:   snapBody,
				BodySize:   bodySize,
				Error:      ErrHttpError,
			})
		}
		return ErrHttpError
	}

	// ── 8. @gg-export variable extraction (2xx only) ─────────────────────
	// Each directive is evaluated in declaration order. The first extraction
	// failure aborts the journey: the variable would be empty/wrong, making
	// every downstream step invalid.
	if mem != nil && len(spec.Exports) > 0 {
		for _, d := range spec.Exports {
			val, exErr := Extract(respBody, d)
			if exErr != nil {
				extractErr := fmt.Errorf("%w: %s: %v", ErrExtractionFailed, d.VarName, exErr)
				e.logCall(spec.Method, resolvedURL, resp.StatusCode, duration, extractErr)
				e.counters.incFailure(shard)
				if e.recorder != nil {
					// Include the full body so the operator can debug the
					// missing path / regex regardless of the sample gate.
					e.recorder.Record(snap.RecordEntry{
						Timestamp:  start,
						Method:     spec.Method,
						URL:        resolvedURL,
						StatusCode: resp.StatusCode,
						Duration:   duration,
						Headers:    resp.Header,
						RespBody:   respBody,
						BodySize:   int64(len(respBody)),
						Error:      extractErr,
					})
				}
				return extractErr
			}
			mem.Set(d.VarName, val)
		}
	}

	// ── 9. Log + count success ────────────────────────────────────────────
	e.logCall(spec.Method, resolvedURL, resp.StatusCode, duration, nil)
	e.counters.incSuccess(shard)

	// ── 10. Snap record ───────────────────────────────────────────────────
	if e.recorder != nil {
		snapBody := respBody
		if !doSample {
			snapBody = nil
		}
		e.recorder.Record(snap.RecordEntry{
			Timestamp:  start,
			Method:     spec.Method,
			URL:        resolvedURL,
			StatusCode: resp.StatusCode,
			Duration:   duration,
			Headers:    resp.Header,
			RespBody:   snapBody,
			BodySize:   bodySize,
		})
	}

	return nil
}

// recordLatency appends the duration to the lock-free latency ring buffer and
// adds the millisecond value to the sharded totalLatency counter.
//
// Ring-buffer write protocol (lock-free):
//  1. Atomically claim a slot: idx = latBuf.n.Add(1) - 1
//  2. Compute ring position: pos = idx % cap(buf)
//  3. Store the IEEE-754 bit representation of the ms value.
//
// Wrapping at capacity (pos = idx % cap) prevents unbounded growth. A slot
// overwritten mid-flight by a subsequent writer produces at most one slightly
// stale percentile sample — acceptable for a display-only metric.
//
// Sub-millisecond durations are rounded up to 1 ms for the sharded counter so
// that the counter is always non-zero after any real request. The float64 ring
// buffer retains the true (possibly sub-ms) value for accurate percentiles.
func (e *Engine) recordLatency(shard int, d time.Duration) {
	ms := float64(d) / float64(time.Millisecond)
	shardMs := int64(ms)
	if shardMs == 0 && d > 0 {
		shardMs = 1 // round sub-ms up so the counter is always non-zero
	}
	e.counters.addLatency(shard, shardMs)

	// Lock-free ring-buffer write.
	lb := e.latBuf.Load()
	if lb != nil && len(lb.buf) > 0 {
		idx := lb.n.Add(1) - 1
		pos := idx % int64(len(lb.buf))
		lb.buf[pos].Store(math.Float64bits(ms))
	}
}
