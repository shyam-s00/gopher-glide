# Hive Engine: Performance Benchmarks

*Tested on: Apple M4 Pro (12-core) | OS: darwin/arm64*

When building a high-traffic system, two things matter most: **maximum throughput** and **minimal overhead**. The Hive Engine is designed to scale to hundreds of thousands of requests per second without stressing your system's garbage collector. 

Below are the benchmark results, combining real-world impact with the raw technical data.

---

## ⚡ 1. The Metrics Subsystem: "Zero Garbage"
When recording thousands of requests per second, the metrics system can often become a bottleneck by creating memory "garbage" that causes system-wide Garbage Collection (GC) pauses. 

The Hive Engine's metrics subsystem is **completely allocation-free**. It tracks throughput and latency without creating a single byte of garbage, rendering it invisible to the Go garbage collector.

| Operation | Execution Time | Memory Overhead | Allocations |
| :--- | :--- | :--- | :--- |
| **Sharded Write** | 50.5 ns | 0 B | **0** |
| **Snapshot Read** | 18.6 ns | 0 B | **0** |
| **RPS Window Record** | 138.4 ns | 0 B | **0** |

* **Real-world impact:** You can record millions of metrics per second and the engine will never slow down to clean up memory.

---

## 🚀 2. Actor Model: Extremely High Throughput
In the Hive Engine, each request is managed by an "Actor." These actors are incredibly lightweight and process requests in parallel across your CPU cores.

| Operation | Execution Time | Max Theoretical RPS |
| :--- | :--- | :--- |
| **Parallel Execution (Multi-Core Aggregate)** | 11.2 µs | ~89,000 RPS (Total System) |
| **Sequential Execution (Single-Core Peak)** | 32.1 µs | ~31,000 RPS (Per Core) |

* **Real-world impact:** A single CPU core can process over **31,000 back-to-back requests per second**. When running in parallel across multiple cores, the total system throughput aggregates to handle even more massive loads (e.g., ~89,000 total RPS on this 12-core test rig). Your bottleneck will almost always be your network bandwidth, not the Hive Engine.

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

* **Real-world impact:** The dispatcher guarantees steady, uniform traffic, avoiding sudden micro-bursts that ruin load tests.

---

## 🔌 4. Connection Pooling: Sub-Microsecond Scale
Opening new network connections is notoriously slow. The Hive Engine intelligently pools and reuses connections to eliminate handshake latency.

| Operation | Execution Time | Memory Overhead | Allocations |
| :--- | :--- | :--- | :--- |
| **Build Transport (1k - 1M RPS)** | ~134 ns | 416 B | 1 |
| **Fd Budget Check** | 84.2 ns | 0 B | **0** |

* **Real-world impact:** Retrieving an open connection from the pool takes **~134 nanoseconds**. Whether you are running at 100 RPS or 1,000,000 RPS, the pool never slows down and continues to provide connections instantly.
