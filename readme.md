<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/ggToolIcon_dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="docs/assets/ggToolIcon.svg">
    <img alt="Gopher Glide Logo" src="docs/assets/ggToolIcon.svg" width="250" />
  </picture>
</div>

<h1 align="center">Gopher-Glide (gg) 🚀</h1>
<p align="center"><b>Run your IDE `.http` files under heavy concurrent load. Zero scripting required.</b></p>

<p align="center">
  <a href="https://github.com/shyam-s00/gopher-glide/actions/workflows/ci.yml"><img src="https://github.com/shyam-s00/gopher-glide/actions/workflows/ci.yml/badge.svg" alt="Build"></a>
  <a href="https://github.com/shyam-s00/gopher-glide/releases/latest"><img src="https://img.shields.io/github/v/release/shyam-s00/gopher-glide?sort=semver&label=release" alt="Release"></a>
  <a href="https://codecov.io/gh/shyam-s00/gopher-glide"><img src="https://codecov.io/gh/shyam-s00/gopher-glide/graph/badge.svg" alt="codecov"></a>
  <a href="https://goreportcard.com/report/github.com/shyam-s00/gopher-glide"><img src="https://goreportcard.com/badge/github.com/shyam-s00/gopher-glide" alt="Go Report Card"></a>
  <a href="https://plugins.jetbrains.com/plugin/30983-gopher-glide"><img src="https://img.shields.io/jetbrains/plugin/v/dev.gopherglide.gg-plugin" alt="JetBrains Plugin Version"></a>
</p>

<p align="center">
  <img src="docs/assets/demo.gif" alt="Gopher Glide TUI Demo" width="800" />
</p>

---

## 🤷‍♂️ Why not just use wrk, k6, or JMeter?

If you want to run a quick concurrency test on an endpoint, you usually have to write a custom JavaScript file for `k6`, a Python script for `Locust`, or learn a heavy configuration language. 

**Gopher-Glide (gg)** is an API traffic simulator that lets you skip the boilerplate. It directly executes standard IDE `.http` REST Client files that you probably already have sitting in your workspace. 

| Feature | Gopher-Glide (`gg`) | k6 / Locust | wrk / hey / vegeta |
| :--- | :--- | :--- | :--- |
| **Scripting** | **None** (Reads `.http` natively) | JavaScript / Python | None (CLI flags only) |
| **Traffic Control** | **30-FPS Interactive TUI** (Arrow keys) | Requires configs | Fixed concurrency only |
| **CI/CD Assertions** | **Semantic JSON Diffing** (`gg snap`) | Pass/Fail Thresholds | Raw latencies only |
| **Built-in Profiles** | **Yes** (`--profile flash-sale`) | Requires scripting | No |
| **IDE Integration** | **Native JetBrains Plugin** | External scripts | External tools |
| **Performance** | **30k+ RPS per core (Actor Model)** | Medium-High | Extremely High |

👉 **[Read the Full Documentation](https://gopherglide.dev/)**

---

## ⚡ 30-Second Quickstart

### 1. Install via Homebrew
```bash
brew install shyam-s00/tap/gg
```

### 2. Point it at any `.http` file
Create a simple `api.http` file (or use an existing one from your IDE):
```http
GET http://localhost:8080/health
Accept: application/json
```

### 3. Run the simulation
Use a built-in profile to automatically shape the traffic (e.g., ramping up, sustaining, and cooling down):
```bash
gg --hive-engine --profile flash-sale --http-file api.http
```

You will instantly drop into the newly polished, butter-smooth 30-FPS Interactive Chaos TUI where you can monitor latencies and adjust the load in real-time without stutter or lag.

---

## 🛠 Feature Deep-Dives

### 🐝 The Hive Engine (Zero-Allocation Architecture)
The core of `gg` is built on a pure-Go lock-free Actor Model. By isolating connections into ultra-lightweight Goroutines and tracking metrics via sharded lock-free atomics, `gg` operates at **`0 allocs/op`** on the hot path. This means you can comfortably push over **30,000+ RPS** on a standard developer machine without garbage-collection pauses destroying your latency percentiles.

> **⚔️ Benchmark:** At 30,000 RPS target load, `gg` consumes **40% less memory** (1.42 GB vs 2.38 GB) and delivers **3x more successful requests** than `k6`. `gg`'s dynamic Adaptive Backpressure acts as an intelligent edge proxy—gracefully throttling excess traffic when the target server breaks to ensure maximum goodput, instead of blindly hammering the network into a deadlock. [Read the full benchmark breakdown](docs/BENCHMARKS.md) to see how `gg` safely simulates massive traffic.

### 🔬 Semantic Regression Gates (`gg snap`)
Don't just test if your API is slow—test if it's broken. By passing the `--snap` flag, Gopher-Glide infers your API's JSON schema in real-time. You can then use `gg snap assert` in your CI/CD pipeline to automatically break the build if a pull request spikes latency or alters a JSON payload contract.

### 🔌 JetBrains IDE Integration 
Gopher Glide features an official [JetBrains plugin](https://plugins.jetbrains.com/plugin/30983-gopher-glide) that brings load testing directly into your IDE. Run profiles directly from your `.http` files via the gutter icon, and explore semantic diffs using the Snap UI Tool Window.

### 🔄 Stateful Journeys & Variable Extraction
No more writing custom JavaScript to test multi-step workflows. `gg` natively understands JetBrains HTTP variable extraction syntax. You can authenticate, extract a token using `# @gg-export token = jsonpath: $.data.token`, and instantly inject it into the next request using `{{token}}`.

---

## Planned Features
- [ ] **Chaos / Fault Injection** — Simulate poor network conditions (3G packet loss, latency jitter) to see how APIs handle degradation.
- [ ] **Distributed Simulation Mesh** — Run `gg worker` nodes across multiple servers/regions, controlled by a central `gg master` UI.

## License
[MIT](LICENSE)
