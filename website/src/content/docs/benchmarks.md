---
title: "Benchmarks"
---
# Hive Engine: Performance Benchmarks

* **Primary Test Rig:** Apple M4 Pro (12-core) | OS: `darwin/arm64`
* **Standard CI Rig / Laptop Expected Multiplier:** ~2.5x to 3x execution time.

> **Note:** The benchmarks below were recorded on high-end Apple Silicon. While a standard 2-core x86 CI runner or mid-range laptop will have slightly higher execution times (typically 2.5x - 3x), the engine's lock-free design ensures they still comfortably clear the >50,000 RPS ceiling claimed on the homepage. We strongly encourage you to run the reproduction commands on your own target hardware.


---

## 🌍 Cross-Platform Scaling (Mac vs. Linux vs. CI)

Gopher-Glide is engineered to scale predictably based on the host hardware's raw instructions-per-cycle (IPC), not "software magic." The engine extracts maximum value from the CPU while remaining resilient across different operating systems and virtualization layers.

Here is how Gopher-Glide performs across three vastly different environments: **Apple M4 Pro** (Bare-metal Darwin), **AMD Ryzen 5600U** (Bare-metal Linux), and **AMD EPYC** (Virtualized Linux / GitHub CI).

| Metric | Apple M4 Pro (Mac) | AMD Ryzen 5 (Linux) | AMD EPYC (CI VM) |
| :--- | :--- | :--- | :--- |
| **Max Single-Core RPS** | 🚀 **31,357** | 7,813* | 11,420 |
| **Pacing Precision (50ms)** | `52.11 ms` | `52.24 ms` | `51.99 ms` |
| **Sharded Write (Metrics)** | `48.60 ns` | ⚡ **`27.87 ns`** | `33.10 ns` |
| **Garbage Generated** | **0 B/op** | **0 B/op** | **0 B/op** |

> *Note: All benchmarks above were stabilized over a sustained 5-second run (`-benchtime=5s`). The Ryzen 5 laptop (a 15W mobile chip) shows a lower sustained RPS due to expected thermal power-limit (PL1) throttling. In short 1-second burst tests, it actually peaks at ~14,700 RPS.*

* **Hardware-Bound, Not Software-Bound:** Throughput scales linearly with CPU power. The Apple Silicon M4 Pro pushes massive sustained single-core RPS, while the AMD Ryzen/EPYC chips leverage advanced cache coherency for blazing-fast metric shard writes (sub-30ns).
* **Flawless Pacing Everywhere:** The Smooth Dispatcher reliably hits sub-second windows (e.g., ~52ms for a 50ms target) across bare-metal macOS, bare-metal Linux, and virtualized hypervisors.
* **True Zero-Garbage:** The engine creates exactly `0 allocs/op` on every OS and architecture, ensuring users won't suffer from surprise GC pauses just because they migrated to a standard cloud runner.

---

When building a high-traffic system, two things matter most: **maximum throughput** and **minimal overhead**. The Hive Engine is designed to scale to hundreds of thousands of requests per second without stressing your system's garbage collector.

---

## ⚔️ Gopher Glide vs. k6 (Resource Efficiency)

Because `k6` scales concurrency by spinning up embedded JavaScript Virtual Machines (Goja) for every virtual user, its resource footprint scales aggressively. Gopher Glide operates entirely via native Go actors and lock-free atomics, completely decoupling high throughput from memory and CPU bloat.

### 🧠 Memory Footprint (30-second local benchmark)

| Target RPS | Gopher Glide (`gg`) | Grafana `k6` | Memory Difference |
| :--- | :--- | :--- | :--- |
| **5,000 RPS** | ~106 MB | ~464 MB | `k6` uses **4.3x** more RAM |
| **10,000 RPS** | **~186 MB** | **~796 MB** | `k6` uses **4.2x** more RAM |

### ⚡ CPU & Kernel Overhead (At 10,000 RPS)

| Metric | Gopher Glide (`gg`) | Grafana `k6` | The Difference |
| :--- | :--- | :--- | :--- |
| **Total CPU Time** (`user` + `sys`) | **38.5s** | 52.2s | `gg` requires **26% less CPU time** |
| **Kernel Time** (`sys`) | **14.0s** | 26.9s | `gg` spends **48% less time** trapped in OS system calls |
| **Context Switches** (Involuntary) | **1.16 Million** | 3.16 Million | `gg` suffers **almost 3x fewer** OS context switches |
| **Page Faults** (Memory) | **0** | 363 | `gg` achieves perfect memory localization |

* **The Takeaway:** Gopher Glide requires roughly **80 MB** of additional RAM to double its throughput from 5k to 10k RPS, while `k6` requires an additional **332 MB**. Furthermore, because `gg` uses Go's user-space M:N scheduling (Goroutines) and zero-allocation hot paths, it drastically reduces kernel syscalls and OS context switching. This ensures your CI/CD runner is dedicated to pushing network traffic—not fighting a JavaScript VM's garbage collector.

---

## 🛡️ Gopher Glide vs. k6 (Goodput & Adaptive Backpressure)

While the previous section showed how efficient `gg` is under normal load, this benchmark tests what happens when the target server **fails**.

Because `k6` operates as a traditional "Closed Model" load tester, it scales concurrency by blindly hammering the target server until it hits a wall. Gopher Glide operates as a true "Open Model" with mathematical **Adaptive Backpressure**—it natively protects the target server from catastrophic deadlocks.

In a saturation test pushing an impossible target of **30,000 RPS** (attempting ~900k total requests over 30 seconds), both engines correctly identified the physical limit of the target NGINX server (delivering exactly ~92,000 requests to the network before the server choked). However, the *outcomes* were vastly different:

### 🧠 The "Mic Drop" Metric: Successful Goodput

| Metric | Gopher Glide (`gg`) | `k6` | The Difference |
| :--- | :--- | :--- | :--- |
| **Total Requests Sent** | 92,059 | 92,184 | Both hit the exact same physical server bottleneck. |
| **Successful Responses** | **76,140** | 25,753 | `gg` delivered **~3x MORE successful requests**. |
| **Failure Rate** | **17.29%** | 72.06% | `k6` pushed the server into a total deadlock. |

**Why did this happen?**
When `k6` hit the server's limit, it just kept violently spawning virtual users, forcing the target server into a total deadlock where 72% of the connections timed out or were refused. 

When `gg` detected the server slowing down, its **Adaptive Backpressure** instantly engaged. `gg` gracefully throttled the excess traffic locally. Because it stopped slamming the network with useless dead-end connections, the target server was actually able to breathe and successfully process the traffic. `gg` acts like an intelligent edge proxy, maximizing your server's Goodput under extreme duress.

### ⚡ Memory Protection at Saturation

| Engine | Peak Memory (RAM) | Efficiency |
| :--- | :--- | :--- |
| **Gopher Glide (`gg`)** | **1.42 GB** | **40% less RAM** required to handle 10k blocked sockets. |
| **`k6`** | 2.38 GB | Heavy JavaScript VM bloat under deadlock conditions. |

---

## ⚡ 1. The Metrics Subsystem: "Zero Garbage"
When recording thousands of requests per second, the metrics system can often become a bottleneck by creating memory "garbage" that causes system-wide Garbage Collection (GC) pauses. 

The Hive Engine's metrics subsystem is **completely allocation-free**. It tracks throughput and latency without creating a single byte of garbage, rendering it invisible to the Go garbage collector.

| Operation | Execution Time | Memory Overhead | Allocations |
| :--- | :--- | :--- | :--- |
| **Sharded Write** | 50.5 ns | 0 B | **0** |
| **Snapshot Read** | 18.6 ns | 0 B | **0** |
| **RPS Window Record** | 138.4 ns | 0 B | **0** |

> **Reproduce this:** `go test -bench=BenchmarkMetrics_ -run='^$' -benchtime=5s -benchmem ./internal/engine/hive/...`
> **Source:** [`internal/engine/hive/bench_test.go`]

* **Real-world impact:** You can record millions of metrics per second and the engine will never slow down to clean up memory.

---

## 🚀 2. Actor Model: Extremely High Throughput
In the Hive Engine, each request is managed by an "Actor." These actors are incredibly lightweight and process requests in parallel across your CPU cores.

| Operation | Execution Time | Engine Overhead Ceiling |
| :--- | :--- | :--- |
| **Goroutine Dispatch (Per Actor)** | 2.1 µs | N/A |
| **Sequential Execution (Single-Core Peak)** | 32.1 µs | ~31,000 Ops/Sec (Per Core) |

> **Reproduce this:** `go test -bench=BenchmarkEngine_MaxRPS_SingleCore -run='^$' -benchtime=5s ./internal/engine/hive/...`
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

> **Reproduce this:** `go test -bench=BenchmarkDispatch_ -run='^$' -benchtime=2s ./internal/engine/hive/...`
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
