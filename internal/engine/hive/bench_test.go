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
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

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
		_ = e.executeActor(context.Background(), spec, 0, nil)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = e.executeActor(context.Background(), spec, i%numShards, nil)
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
		_ = e.executeActor(context.Background(), spec, 0, nil)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		shard := 0
		for pb.Next() {
			_ = e.executeActor(context.Background(), spec, shard%numShards, nil)
			shard++
		}
	})
}

// BenchmarkActor_WithRecorder measures the additional per-request overhead of
// the --snap pipeline on the actor hot path: e.recorder.Record() plus, for
// sampled requests, draining the response body into a pooled buffer (steps
// 5 and 10 of executeActor) instead of io.Discard.
//
// "no_recorder" mirrors BenchmarkActor_ConnectionReuse and serves as the
// baseline. The other variants attach a snap.DefaultRecorder at different
// sample rates to isolate the Record()-enqueue cost (no_sample) from the
// body-capture cost (sample_5pct / sample_100pct).
func BenchmarkActor_WithRecorder(b *testing.B) {
	srv := fastServer()
	defer srv.Close()
	spec := httpreader.RequestSpec{Method: http.MethodGet, URL: srv.URL}

	cases := []struct {
		name       string
		withRec    bool
		sampleRate float64
	}{
		{"no_recorder", false, 0},
		{"recorder_no_sample", true, 0},
		{"recorder_sample_5pct", true, 0.05},
		{"recorder_sample_100pct", true, 1.0},
	}

	for _, tc := range cases {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			var opts []EngineOption
			var rec *snap.DefaultRecorder
			if tc.withRec {
				rec = snap.NewDefaultRecorder(1 << 20)
				opts = append(opts, WithRecorder(rec), WithSampleRate(tc.sampleRate))
			}
			e := New(opts...)

			// Warm the connection pool before the timed loop.
			for i := 0; i < 10; i++ {
				_ = e.executeActor(context.Background(), spec, 0, nil)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = e.executeActor(context.Background(), spec, i%numShards, nil)
			}
			b.StopTimer()

			if rec != nil {
				_, _ = rec.Finalize(snap.RunMeta{})
			}
		})
	}
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
		_ = e.executeActor(context.Background(), spec, 0, nil)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = e.executeActor(context.Background(), spec, i%numShards, nil)
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "RPS")
}

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
			journeys := []httpreader.Journey{{Specs: []httpreader.RequestSpec{{Method: http.MethodGet, URL: srv.URL}}}}

			b.SetBytes(int64(tc.count)) // actors launched per iteration
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				idx := 0
				h.dispatch(context.Background(),
					SpawnManifest{Count: tc.count, Duration: tc.duration},
					journeys, &idx)
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
			journeys := []httpreader.Journey{{Specs: []httpreader.RequestSpec{{Method: http.MethodGet, URL: srv.URL}}}}

			b.SetBytes(int64(tc.count))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				idx := 0
				h.dispatch(context.Background(),
					SpawnManifest{Count: tc.count, Duration: tc.duration},
					journeys, &idx)
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

	journeys := []httpreader.Journey{{Specs: []httpreader.RequestSpec{{Method: http.MethodGet, URL: srv.URL}}}}
	e := New()
	h := &hatchery{e: e}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manifestCh := make(chan SpawnManifest, 1)
		manifestCh <- SpawnManifest{Count: 1, Duration: 50 * time.Millisecond}
		close(manifestCh)
		// run drains from the channel and dispatches the single manifest.
		_ = h.run(context.Background(), manifestCh, journeys)
		drainActors(e, 2*time.Second)
	}
}

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

// fillErrorRings saturates every shard's error ring with fresh entries.
func fillErrorRings(e *Engine) {
	for shard := 0; shard < numShards; shard++ {
		for i := 0; i < e.maxLogs; i++ {
			e.logCall(shard, "GET", "http://x/a", 500+i%4, time.Millisecond, ErrHttpError)
		}
	}
}

// BenchmarkErrorMix_RingsFull is the cost of one heartbeat's scan when every
// ring is full (worst case for the read).
func BenchmarkErrorMix_RingsFull(b *testing.B) {
	e := New()
	fillErrorRings(e)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := e.RecentErrorMix(10 * time.Second); !ok {
			b.Fatal("no mix")
		}
	}
}

// BenchmarkErrorMix_WithWriters runs the scan while actors keep logging errors
// on every shard, and reports the writers' per-call latency under that load.
func BenchmarkErrorMix_WithWriters(b *testing.B) {
	e := New()
	fillErrorRings(e)
	var stop atomic.Bool
	var wg sync.WaitGroup
	for w := 0; w < numShards; w++ {
		wg.Add(1)
		go func(shard int) {
			defer wg.Done()
			for !stop.Load() {
				e.logCall(shard, "GET", "http://x/a", 503, time.Millisecond, ErrHttpError)
			}
		}(w)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.RecentErrorMix(10 * time.Second)
	}
	b.StopTimer()
	stop.Store(true)
	wg.Wait()
}

// BenchmarkLogCall_WhileMixScans measures writer cost per logCall with and
// without a concurrent heartbeat-rate scan (once per ms, far above the real 1/s).
func BenchmarkLogCall_WhileMixScans(b *testing.B) {
	for _, scanning := range []bool{false, true} {
		name := "no_scan"
		if scanning {
			name = "scan_every_1ms"
		}
		b.Run(name, func(b *testing.B) {
			e := New()
			fillErrorRings(e)
			stop := make(chan struct{})
			var wg sync.WaitGroup
			if scanning {
				wg.Add(1)
				go func() {
					defer wg.Done()
					t := time.NewTicker(time.Millisecond)
					defer t.Stop()
					for {
						select {
						case <-stop:
							return
						case <-t.C:
							e.RecentErrorMix(10 * time.Second)
						}
					}
				}()
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				shard := int(atomic.AddInt32(&benchShard, 1))
				for pb.Next() {
					e.logCall(shard, "GET", "http://x/a", 503, time.Millisecond, ErrHttpError)
				}
			})
			b.StopTimer()
			close(stop)
			wg.Wait()
		})
	}
}

var benchShard int32

// BenchmarkLogCallLatency_PacedWriters runs one writer per shard at ~50k
// logCalls/s each (about 800k RPS in total, well above what the engine
// sustains) for a second, with and without a 100 Hz error-mix scan, and
// reports the writers' logCall tail latency and the scan duration.
func BenchmarkLogCallLatency_PacedWriters(b *testing.B) {
	const gap = 20 * time.Microsecond
	for _, scan := range []bool{false, true} {
		name := "no_scan"
		if scan {
			name = "scan_100hz"
		}
		b.Run(name, func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				e := New()
				fillErrorRings(e)
				var stop atomic.Bool
				var wg sync.WaitGroup
				lat := make([][]time.Duration, numShards)
				for w := 0; w < numShards; w++ {
					wg.Add(1)
					go func(w int) {
						defer wg.Done()
						buf := make([]time.Duration, 0, 1<<17)
						for !stop.Load() {
							t0 := time.Now()
							e.logCall(w, "GET", "http://x/a", 503, time.Millisecond, ErrHttpError)
							buf = append(buf, time.Since(t0))
							for end := time.Now().Add(gap); time.Now().Before(end); {
							}
						}
						lat[w] = buf
					}(w)
				}
				var scans []time.Duration
				if scan {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for !stop.Load() {
							t0 := time.Now()
							e.RecentErrorMix(10 * time.Second)
							scans = append(scans, time.Since(t0))
							time.Sleep(10 * time.Millisecond)
						}
					}()
				}
				time.Sleep(time.Second)
				stop.Store(true)
				wg.Wait()

				var all []time.Duration
				for _, l := range lat {
					all = append(all, l...)
				}
				sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
				pct := func(p float64) float64 { return float64(all[int(float64(len(all)-1)*p)].Nanoseconds()) }
				b.ReportMetric(pct(0.50), "logcall_p50_ns")
				b.ReportMetric(pct(0.99), "logcall_p99_ns")
				b.ReportMetric(pct(0.9999), "logcall_p9999_ns")
				b.ReportMetric(float64(all[len(all)-1].Nanoseconds()), "logcall_max_ns")
				if len(scans) > 0 {
					sort.Slice(scans, func(i, j int) bool { return scans[i] < scans[j] })
					b.ReportMetric(float64(scans[len(scans)/2].Microseconds()), "scan_p50_us")
					b.ReportMetric(float64(scans[len(scans)-1].Microseconds()), "scan_max_us")
				}
			}
		})
	}
}

// BenchmarkErrorMix_TopEndpoints is the heartbeat scan with spec-template
// grouping over full rings of distinct resolved URLs (the worst case).
func BenchmarkErrorMix_TopEndpoints(b *testing.B) {
	e := New()
	e.epMatch.Store(newEndpointMatcher([]httpreader.RequestSpec{
		{Method: "GET", URL: "http://x/users/{{id}}"},
		{Method: "POST", URL: "http://x/checkout"},
		{Method: "GET", URL: "http://x/orders/{{id}}/items/{{n}}"},
	}))
	for shard := 0; shard < numShards; shard++ {
		for i := 0; i < e.maxLogs; i++ {
			switch i % 3 {
			case 0:
				e.logCall(shard, "GET", "http://x/users/"+strconv.Itoa(i*shard), 500, time.Millisecond, ErrHttpError)
			case 1:
				e.logCall(shard, "POST", "http://x/checkout", 503, time.Millisecond, ErrHttpError)
			default:
				e.logCall(shard, "GET", "http://x/orders/"+strconv.Itoa(i)+"/items/2", 429, time.Millisecond, ErrHttpError)
			}
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if mix, ok := e.RecentErrorMix(10 * time.Second); !ok || len(mix.TopEndpoints) != 3 {
			b.Fatal("no endpoints")
		}
	}
}

// BenchmarkHealthSince is one windowed-health read: a counter sweep plus a
// p99 over the window's samples.
func BenchmarkHealthSince(b *testing.B) {
	e := New()
	buf := newLatencyBuf(64 * 1024)
	e.latBuf.Store(&buf)
	now := time.Now()
	e.sampleHealth(now.Add(-6 * time.Second))
	for i := 0; i < 30000; i++ {
		e.counters.incSuccess(i)
		e.recordLatency(i, time.Duration(i%50)*time.Millisecond)
	}
	e.sampleHealth(now.Add(-time.Second))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := e.healthSince(now, 5*time.Second); !ok {
			b.Fatal("no window")
		}
	}
}
