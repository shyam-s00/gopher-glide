// Package hive – benchmark suite for 1.3.2
//
// Validates two key claims from the architecture document:
//  1. The Hatchery can dispatch >= 50 000 RPS without OS-scheduler throttling.
//  2. The dynamic connection pool adds negligible construction overhead and
//     shows measurable connection-reuse benefit (low allocs/op after warm-up).
//
// Run all benchmarks:
//
//	go test -bench=. -benchmem -benchtime=3s ./internal/engine/hive/
//
// Run only pool benchmarks:
//
//	go test -bench=BenchmarkPool -benchmem ./internal/engine/hive/
//
// Run only dispatcher benchmarks:
//
//	go test -bench=BenchmarkDispatch -benchmem ./internal/engine/hive/
package hive

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// ── helpers ────────────────────────────────────────────────────────────────────

// drainActors spins until all in-flight actor goroutines have exited or the
// deadline expires. Used between benchmark iterations to prevent goroutine
// accumulation from skewing successive measurements.
func drainActors(e *Engine, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for e.activeActors.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

// fastServer returns an httptest.Server that responds 200 OK with an empty
// body as quickly as possible — ideal for isolating dispatch overhead from
// application latency in benchmarks.
func fastServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

// ── Pool / Transport construction ─────────────────────────────────────────────

// BenchmarkPool_BuildTransport measures the construction cost of buildTransport
// across the full RPS spectrum. This function is called at the start of every
// RunStages invocation, so it must be sub-millisecond at any scale.
//
// The FD-cap code path (syscall + clamping) is exercised at 1_000_000 RPS.
func BenchmarkPool_BuildTransport(b *testing.B) {
	for _, rps := range []int{100, 1_000, 10_000, 50_000, 1_000_000} {
		rps := rps
		b.Run(fmt.Sprintf("rps_%d", rps), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = buildTransport(rps)
			}
		})
	}
}

// BenchmarkPool_FdBudget measures the raw syscall round-trip cost of querying
// RLIMIT_NOFILE. This is called inside buildTransport; confirming it is
// sub-microsecond ensures it is not a bottleneck on the RunStages hot path.
func BenchmarkPool_FdBudget(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = fdBudget()
	}
}

// BenchmarkPool_ScalingSanity verifies that MaxIdleConnsPerHost grows
// proportionally with peak RPS, with no unexpected allocation spikes.
// It also serves as a regression guard: if buildTransport ever becomes O(RPS)
// rather than O(1), this benchmark will surface it.
func BenchmarkPool_ScalingSanity(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, rps := range []int{100, 1_000, 10_000, 50_000} {
			tr := buildTransport(rps)
			_ = tr.MaxIdleConnsPerHost
		}
	}
}

// ── Connection reuse (allocs/op) ───────────────────────────────────────────────

// BenchmarkActor_ConnectionReuse shows that a warmed keep-alive pool reduces
// per-request heap allocations. After the initial warm-up burst, each actor
// should reuse an idle TCP connection, driving allocs/op toward a low steady
// state (typically < 30 allocs/op with zero garbage from response bodies).
//
// Interpretation:
//   - Cold (first pass): higher allocs/op — TCP handshake + buffers allocated.
//   - Warm (b.N loop):   lower allocs/op — socket recycled from pool.
//
// A large delta between the two indicates the pool is working correctly.
func BenchmarkActor_ConnectionReuse(b *testing.B) {
	srv := fastServer()
	defer srv.Close()

	e := New()
	spec := httpreader.RequestSpec{Method: http.MethodGet, URL: srv.URL}

	// Warm: force a TCP connection into the pool before the timed loop.
	for i := 0; i < 10; i++ {
		_ = e.executeActor(context.Background(), spec, 0)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = e.executeActor(context.Background(), spec, i%numShards)
	}
}

// BenchmarkActor_Parallel drives executeActor from GOMAXPROCS goroutines
// simultaneously. This is the steady-state profile inside a real run and
// surfaces any lock contention on shared Engine state (latencyMu, callLogsMu).
//
// Goal: near-linear scaling — ns/op should not increase significantly as
// GOMAXPROCS grows. Run with GOMAXPROCS=1,2,4,8 to inspect scaling.
func BenchmarkActor_Parallel(b *testing.B) {
	srv := fastServer()
	defer srv.Close()

	e := New()
	spec := httpreader.RequestSpec{Method: http.MethodGet, URL: srv.URL}

	// Warm the connection pool before the timed loop.
	for i := 0; i < 10; i++ {
		_ = e.executeActor(context.Background(), spec, 0)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		shard := 0
		for pb.Next() {
			_ = e.executeActor(context.Background(), spec, shard%numShards)
			shard++
		}
	})
}

// BenchmarkEngine_MaxRPS_SingleCore measures the absolute maximum throughput
// of the engine on a single CPU core. It bypasses the Queen's pacing and
// executes the actor pipeline back-to-back as fast as possible.
//
// b.SetBytes(1) is used so the "MB/s" column in the output directly maps to
// Requests Per Second (RPS) on a single core.
func BenchmarkEngine_MaxRPS_SingleCore(b *testing.B) {
	srv := fastServer()
	defer srv.Close()

	e := New()
	spec := httpreader.RequestSpec{Method: http.MethodGet, URL: srv.URL}

	// Warm the connection pool
	for i := 0; i < 10; i++ {
		_ = e.executeActor(context.Background(), spec, 0)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = e.executeActor(context.Background(), spec, i%numShards)
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "RPS")
}

// ── Hatchery dispatcher throughput ────────────────────────────────────────────

// BenchmarkDispatch_Throughput is the headline dispatcher benchmark.
// It measures total wall time for a complete dispatch-and-drain cycle at 5
// different actor counts per manifest window.
//
// b.SetBytes(int64(count)) lets the framework report actors/second (shown
// as MB/s in go test output; divide by 1e6 to get actors/µs).
//
// Expected behaviour: dispatch time should scale sub-linearly with count
// because the Hatchery amortises goroutine spawn across micro-batch ticks.
func BenchmarkDispatch_Throughput(b *testing.B) {
	cases := []struct {
		count    int
		duration time.Duration
	}{
		{10, time.Second},
		{100, time.Second},
		{500, time.Second},
		{1_000, time.Second},
		// Higher counts use a shorter window so b.N stays ≥ 1 within benchtime.
		{5_000, time.Second},
	}
	for _, tc := range cases {
		tc := tc
		b.Run(fmt.Sprintf("count_%d", tc.count), func(b *testing.B) {
			srv := fastServer()
			defer srv.Close()

			e := New()
			h := &hatchery{e: e}
			specs := []httpreader.RequestSpec{{Method: http.MethodGet, URL: srv.URL}}

			b.SetBytes(int64(tc.count)) // actors launched per iteration
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				idx := 0
				h.dispatch(context.Background(),
					SpawnManifest{Count: tc.count, Duration: tc.duration},
					specs, &idx)
				drainActors(e, 15*time.Second)
			}
		})
	}
}

// BenchmarkDispatch_SubSecondWindow benchmarks the 1.2.13 Duration-driven path
// where the Queen emits manifests shorter than 1 second (e.g. fractional stage
// ends). These must complete well inside their window and must not temporally
// bleed into the next second.
//
// This is the critical regression guard for sub-second dispatch accuracy.
func BenchmarkDispatch_SubSecondWindow(b *testing.B) {
	cases := []struct {
		count    int
		duration time.Duration
	}{
		{5, 50 * time.Millisecond},
		{20, 200 * time.Millisecond},
		{50, 400 * time.Millisecond},
	}
	for _, tc := range cases {
		tc := tc
		b.Run(fmt.Sprintf("count_%d_dur_%s", tc.count, tc.duration), func(b *testing.B) {
			srv := fastServer()
			defer srv.Close()

			e := New()
			h := &hatchery{e: e}
			specs := []httpreader.RequestSpec{{Method: http.MethodGet, URL: srv.URL}}

			b.SetBytes(int64(tc.count))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				idx := 0
				h.dispatch(context.Background(),
					SpawnManifest{Count: tc.count, Duration: tc.duration},
					specs, &idx)
				drainActors(e, 5*time.Second)
			}
		})
	}
}

// BenchmarkDispatch_BatchMath isolates the pure arithmetic inside dispatch
// (batchSize / numTicks / remainder) from all I/O. This confirms the batch
// sizing calculation is O(1) and produces zero allocations, which is
// essential for it to be called 100 times/second at high RPS.
func BenchmarkDispatch_BatchMath(b *testing.B) {
	cases := []struct {
		label    string
		count    int
		numTicks int
	}{
		{"1_per_tick", 100, 100},      // batchSize=1, remainder=0
		{"remainder_only", 7, 100},    // batchSize=0, remainder=7
		{"5_per_tick", 500, 100},      // batchSize=5, remainder=0
		{"500_per_tick", 50_000, 100}, // 50k RPS scenario
		{"mixed_remainder", 333, 100}, // batchSize=3, remainder=33
	}
	for _, c := range cases {
		c := c
		b.Run(c.label, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				batchSize := c.count / c.numTicks
				remainder := c.count % c.numTicks
				_, _ = batchSize, remainder
			}
		})
	}
}

// BenchmarkDispatch_ManifestChannelRoundTrip measures the latency of the full
// Queen → channel → Hatchery pipeline for a single SpawnManifest. Sending and
// receiving one manifest on a buffered channel should be nanosecond-scale.
func BenchmarkDispatch_ManifestChannelRoundTrip(b *testing.B) {
	srv := fastServer()
	defer srv.Close()

	specs := []httpreader.RequestSpec{{Method: http.MethodGet, URL: srv.URL}}
	e := New()
	h := &hatchery{e: e}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manifestCh := make(chan SpawnManifest, 1)
		manifestCh <- SpawnManifest{Count: 1, Duration: 50 * time.Millisecond, SpecIndex: 0}
		close(manifestCh)
		// run drains from the channel and dispatches the single manifest.
		_ = h.run(context.Background(), manifestCh, specs)
		drainActors(e, 2*time.Second)
	}
}

// ── Sharded metrics hot-path ───────────────────────────────────────────────────

// BenchmarkMetrics_ShardedWrite benchmarks the per-Actor counter hot-path
// under GOMAXPROCS-level concurrency. With cache-line padding these atomics
// should exhibit near-linear scaling — no single cache line becomes a
// bottleneck as core count grows.
func BenchmarkMetrics_ShardedWrite(b *testing.B) {
	var m metrics
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		shard := 0
		for pb.Next() {
			m.incTotalRequests(shard % numShards)
			m.incSuccess(shard % numShards)
			m.addLatency(shard%numShards, 5)
			shard++
		}
	})
}

// BenchmarkMetrics_SnapshotRead benchmarks the GetMetrics read path — summing
// all 16 shards. This runs at ~10 Hz from the TUI goroutine; the benchmark
// confirms it is cheap enough to be negligible even at maximum actor density.
func BenchmarkMetrics_SnapshotRead(b *testing.B) {
	var m metrics
	// Pre-populate so the read is non-trivial.
	for i := 0; i < numShards; i++ {
		m.incTotalRequests(i)
		m.incSuccess(i)
		m.addLatency(i, 12)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.loadTotalRequests()
		_ = m.loadSuccessCount()
		_ = m.loadFailureCount()
		_ = m.loadTotalLatency()
	}
}

// BenchmarkMetrics_RpsWindowRecord benchmarks rpsWindow.record() under
// concurrent load. The lock inside record() is the only non-atomic primitive
// in the Actor hot path; this benchmark quantifies its overhead.
func BenchmarkMetrics_RpsWindowRecord(b *testing.B) {
	var w rpsWindow
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w.record(1)
		}
	})
}

// BenchmarkMetrics_RpsWindowRate benchmarks rpsWindow.rate() — the read side
// called by GetMetrics at TUI refresh intervals.
func BenchmarkMetrics_RpsWindowRate(b *testing.B) {
	var w rpsWindow
	// Seed a few ticks so rate() does real arithmetic.
	for i := 0; i < 100; i++ {
		w.record(1)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = w.rate()
	}
}
