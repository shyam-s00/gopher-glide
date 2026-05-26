# Hive Engine: Performance Benchmarks

* **Primary Test Rig:** Apple M4 Pro (12-core) | OS: `darwin/arm64`
* **Standard CI Rig / Laptop Expected Multiplier:** ~2.5x to 3x execution time.

> **Note:** The benchmarks below were recorded on high-end Apple Silicon. While a standard 2-core x86 CI runner or mid-range laptop will have slightly higher execution times (typically 2.5x - 3x), the engine's lock-free design ensures they still comfortably clear the >50,000 RPS ceiling claimed on the homepage. We strongly encourage you to run the reproduction commands on your own target hardware.

---

## 📊 Live On-Demand Benchmarks

We maintain a standalone GitHub Action pipeline to run performance benchmarks on demand for any branch.

* 🔗 **[View Latest Benchmark Results (Raw Text)](../benchmark_results.txt)**
* 🛠️ **How to Run on a custom branch:**
  1. Go to the [Manual Benchmark Action on GitHub](https://github.com/shyam-s00/gopher-glide/actions/workflows/manual-benchmark.yml).
  2. Click the **Run workflow** dropdown.
  3. Select your target branch.
  4. Set `Publish results to the website` to `true` (to update the link above) or `false` (to run privately and download the results from the run artifacts).
  5. Click **Run workflow**.

---


When building a high-traffic system, two things matter most: **maximum throughput** and **minimal overhead**. The Hive Engine is designed to scale to hundreds of thousands of requests per second without stressing your system's garbage collector.

---

## ⚡ 1. The Metrics Subsystem: "Zero Garbage"
When recording thousands of requests per second, the metrics system can often become a bottleneck by creating memory "garbage" that causes system-wide Garbage Collection (GC) pauses. 

The Hive Engine's metrics subsystem is **completely allocation-free**. It tracks throughput and latency without creating a single byte of garbage, rendering it invisible to the Go garbage collector.

| Operation | Execution Time | Memory Overhead | Allocations |
| :--- | :--- | :--- | :--- |
| **Sharded Write** | 50.5 ns | 0 B | **0** |
| **Snapshot Read** | 18.6 ns | 0 B | **0** |
| **RPS Window Record** | 138.4 ns | 0 B | **0** |

> **Reproduce this:** `go test -bench=BenchmarkMetrics_ -benchtime=5s -benchmem ./internal/engine/hive/...`
> **Source:** [`internal/engine/hive/bench_test.go`]

* **Real-world impact:** You can record millions of metrics per second and the engine will never slow down to clean up memory.

---

## 🚀 2. Actor Model: Extremely High Throughput
In the Hive Engine, each request is managed by an "Actor." These actors are incredibly lightweight and process requests in parallel across your CPU cores.

| Operation | Execution Time | Engine Overhead Ceiling |
| :--- | :--- | :--- |
| **Goroutine Dispatch (Per Actor)** | 2.1 µs | N/A |
| **Sequential Execution (Single-Core Peak)** | 32.1 µs | ~31,000 Ops/Sec (Per Core) |

> **Reproduce this:** `go test -bench=BenchmarkEngine_MaxRPS_SingleCore -benchtime=5s ./internal/engine/hive/...`
> **Source:** [`internal/engine/hive/bench_test.go`]

* **Real-world impact:** The Hive Engine operates with such minimal overhead that a single CPU core can dispatch and cycle over 31,000 requests per second in isolation. By operating completely lock-free, the *engine itself* scales linearly across all available CPU cores. However, in practice, your ultimate RPS limit will be governed entirely by your OS network stack, ephemeral ports, and TLS handshakes, rather than the Gopher-Glide software.

---

## 🎯 3. Dispatcher Precision: Flawless Pacing
Load testing isn't just about going fast; it's about going at the *exact speed you requested*. If you ask for 500 RPS, doing 1,000 RPS in half a second is a failure. 

The Hive Engine features a "Smooth Dispatcher" that perfectly paces requests across time windows to ensure your target server receives the exact traffic shape you defined.

| Simulation Window | Target Count | Actual Execution Time | Accuracy |
| :--- | :--- | :--- | :--- |
| **50ms Window** | 5 | 52.0ms | Highly Accurate |
| **200ms Window** | 20 | 202.2ms | Highly Accurate |
| **400ms Window** | 50 | 401.9ms | Highly Accurate |
| **1 Second Window** | 5,000 | ~1.003s | Highly Accurate |

> **Reproduce this:** `go test -bench=BenchmarkDispatch_ -benchtime=2s ./internal/engine/hive/...`
> **Source:** [`internal/engine/hive/bench_test.go`]

* **Real-world impact:** Many load testers suffer from "micro-bursting"—if asked for 100 RPS, they fire 100 requests in the first 10ms and sleep for 990ms, overwhelming the target server's buffers and skewing latency metrics. The Hive Engine guarantees steady, uniform traffic distribution across the entire second.

**Visualization (Target: 100 RPS)**
```text
Typical Naive Load Tester (Micro-bursting)
  0ms ┝━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ (100 requests fired instantly)
100ms │
200ms │
300ms │ 
...   │ (sleeping to maintain average RPS)

Gopher-Glide's Smooth Dispatcher
  0ms ┝━━━━ (10 requests)
100ms ┝━━━━ (10 requests)
200ms ┝━━━━ (10 requests)
300ms ┝━━━━ (10 requests)
...   ┝━━━━ (continuous even pacing)
```

---

## 🔌 4. Connection Pooling: Sub-Microsecond Scale
Opening new network connections is notoriously slow. The Hive Engine intelligently pools and reuses connections to eliminate handshake latency.

| Operation | Execution Time | Memory Overhead | Allocations |
| :--- | :--- | :--- | :--- |
| **Build Transport (1k - 1M RPS)** | ~134 ns | 416 B | 1 |
| **Fd Budget Check** | 84.2 ns | 0 B | **0** |

* **Real-world impact:** Retrieving an open connection from the pool takes **~134 nanoseconds**. Whether you are running at 100 RPS or 1,000,000 RPS, the pool never slows down and continues to provide connections instantly.
