// Benchmark suite for internal/snap — the --snap recording pipeline.
//
// These benchmarks cover the per-response hot path (Record → Sanitize →
// endpointAcc.record) that runs once per HTTP response when snapping is
// enabled, plus the once-per-run aggregation cost (toEndpointSnap, InferSchema,
// Finalize).
//
// Run all benchmarks:
//
//	go test -bench=. -benchmem -benchtime=3s ./internal/snap/
package snap

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// newBenchAcc mirrors DefaultRecorder.newAcc() for use without a full recorder.
func newBenchAcc(maxSamples int) *endpointAcc {
	return &endpointAcc{
		statusCodes:    make(map[int]int64),
		latenciesMs:    make([]float64, 0, 64),
		bodySamples:    make([][]byte, 0, maxSamples),
		maxBodySamples: maxSamples,
	}
}

// ── Sanitizer ──────────────────────────────────────────────────────────────────

// BenchmarkSanitize_DefaultSanitizer measures the per-entry cost of stripping
// sensitive headers. This runs on the single drain goroutine for every
// recorded entry, so it must stay cheap relative to channel throughput.
func BenchmarkSanitize_DefaultSanitizer(b *testing.B) {
	s := NewDefaultSanitizer()
	entry := RecordEntry{
		Method: "GET",
		URL:    "/api/users",
		Headers: http.Header{
			"Authorization": []string{"Bearer xxxxxxxxxxxxxxxx"},
			"Content-Type":  []string{"application/json"},
			"X-Request-Id":  []string{"abc-123-def-456"},
			"Cookie":        []string{"session=xyz; theme=dark"},
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Sanitize(entry)
	}
}

// BenchmarkSanitize_NoHeaders measures the fast-path (no Headers set), which
// short-circuits before any map allocation.
func BenchmarkSanitize_NoHeaders(b *testing.B) {
	s := NewDefaultSanitizer()
	entry := RecordEntry{Method: "GET", URL: "/api/users"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Sanitize(entry)
	}
}

// BenchmarkSanitize_Noop benchmarks the zero-overhead path used when
// sanitization is disabled via WithSanitizer(NoopSanitizer{}).
func BenchmarkSanitize_Noop(b *testing.B) {
	s := NoopSanitizer{}
	entry := RecordEntry{
		Method:  "GET",
		URL:     "/api/users",
		Headers: http.Header{"Authorization": []string{"Bearer xxx"}},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Sanitize(entry)
	}
}

// ── endpointAcc.record / recordBody ───────────────────────────────────────────

// BenchmarkEndpointAcc_Record covers the three regimes recordBody can be in:
//   - no body sample at all (most requests, when sample-rate doesn't trigger)
//   - reservoir still filling (append path)
//   - reservoir full (Knuth Algorithm R replacement path)
func BenchmarkEndpointAcc_Record(b *testing.B) {
	base := RecordEntry{
		StatusCode: 200,
		Duration:   12 * time.Millisecond,
		BodySize:   512,
	}
	body := []byte(`{"id":1,"name":"alice","email":"alice@example.com","active":true}`)

	b.Run("no_body_sample", func(b *testing.B) {
		a := newBenchAcc(DefaultMaxBodySamples)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			a.record(base)
		}
	})

	b.Run("reservoir_filling", func(b *testing.B) {
		entry := base
		entry.RespBody = body
		// Reservoir capacity exceeds b.N so every call takes the append path.
		a := newBenchAcc(b.N + 1)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			a.record(entry)
		}
	})

	b.Run("reservoir_full", func(b *testing.B) {
		entry := base
		entry.RespBody = body
		a := newBenchAcc(DefaultMaxBodySamples)
		for i := 0; i < DefaultMaxBodySamples; i++ {
			a.record(entry)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			a.record(entry)
		}
	})
}

// ── endpointAcc.toEndpointSnap ─────────────────────────────────────────────────

// BenchmarkEndpointAcc_ToEndpointSnap measures the once-per-run aggregation
// cost (sorting latencies/payload sizes + percentile computation) across a
// range of run sizes. This runs once per endpoint inside Finalize.
func BenchmarkEndpointAcc_ToEndpointSnap(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000, 100_000} {
		n := n
		b.Run(fmt.Sprintf("requests_%d", n), func(b *testing.B) {
			template := newBenchAcc(DefaultMaxBodySamples)
			for i := 0; i < n; i++ {
				template.totalCount++
				template.statusCodes[200]++
				template.latenciesMs = append(template.latenciesMs, float64(i%50))
				template.payloadSizes = append(template.payloadSizes, float64(100+i%900))
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = template.toEndpointSnap("GET:/api/users")
			}
		})
	}
}

// ── InferSchema ────────────────────────────────────────────────────────────────

// BenchmarkInferSchema measures JSON schema inference cost over the body
// sample reservoir, run once per endpoint (with stored samples) in Finalize.
func BenchmarkInferSchema(b *testing.B) {
	sample := []byte(`{"id":123,"name":"alice","email":"alice@example.com","active":true,"tags":["a","b","c"],"meta":{"created_at":"2024-01-01T00:00:00Z","score":4.5}}`)
	for _, n := range []int{1, 50, 200} {
		n := n
		b.Run(fmt.Sprintf("samples_%d", n), func(b *testing.B) {
			bodies := make([][]byte, n)
			for i := range bodies {
				bodies[i] = sample
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = InferSchema(bodies)
			}
		})
	}
}

// ── DefaultRecorder.Record (hot path) ─────────────────────────────────────────

// BenchmarkRecorder_Record measures the cost of the non-blocking channel send
// from concurrent actor goroutines — the only part of the snap pipeline that
// runs on the engine's request hot path.
func BenchmarkRecorder_Record(b *testing.B) {
	r := NewDefaultRecorder(1 << 20) // large buffer so drops don't skew results
	entry := RecordEntry{
		Method:     "GET",
		URL:        "/api/users",
		StatusCode: 200,
		Duration:   3 * time.Millisecond,
		BodySize:   256,
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.Record(entry)
		}
	})
	b.StopTimer()
	_, _ = r.Finalize(RunMeta{})
}

// ── DefaultRecorder.Finalize (end-to-end) ─────────────────────────────────────

// BenchmarkRecorder_Finalize measures the full pipeline — Record, drain,
// sanitize, accumulate, and Finalize — across a range of endpoint counts and
// per-endpoint request volumes. b.SetBytes lets the framework report
// effective entries/sec as MB/s.
func BenchmarkRecorder_Finalize(b *testing.B) {
	cases := []struct {
		endpoints   int
		perEndpoint int
	}{
		{1, 1_000},
		{10, 1_000},
		{50, 1_000},
	}
	for _, c := range cases {
		c := c
		b.Run(fmt.Sprintf("endpoints_%d_x_%d", c.endpoints, c.perEndpoint), func(b *testing.B) {
			b.SetBytes(int64(c.endpoints * c.perEndpoint))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r := NewDefaultRecorder(c.endpoints * c.perEndpoint)
				for e := 0; e < c.endpoints; e++ {
					url := fmt.Sprintf("/api/resource%d", e)
					for j := 0; j < c.perEndpoint; j++ {
						r.Record(RecordEntry{
							Method:     "GET",
							URL:        url,
							StatusCode: 200,
							Duration:   time.Duration(j%50) * time.Millisecond,
							BodySize:   int64(100 + j%900),
						})
					}
				}
				_, _ = r.Finalize(RunMeta{})
			}
		})
	}
}
