<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/ggToolIcon_dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="docs/assets/ggToolIcon.svg">
    <img alt="Gopher Glide Logo" src="docs/assets/ggToolIcon.svg" width="250" />
  </picture>
</div>

<h1 align="center">Gopher-Glide (gg) 🚀</h1>
<p align="center"><b>High-fidelity API traffic simulation from your IDE. Beyond brute-force load testing.</b></p>

<p align="center">
  <a href="https://github.com/shyam-s00/gopher-glide/actions/workflows/ci.yml"><img src="https://github.com/shyam-s00/gopher-glide/actions/workflows/ci.yml/badge.svg" alt="Build"></a>
  <a href="https://github.com/shyam-s00/gopher-glide/releases/latest"><img src="https://img.shields.io/github/v/release/shyam-s00/gopher-glide?sort=semver&label=release" alt="Release"></a>
  <a href="https://codecov.io/gh/shyam-s00/gopher-glide"><img src="https://codecov.io/gh/shyam-s00/gopher-glide/graph/badge.svg" alt="codecov"></a>
  <a href="https://goreportcard.com/report/github.com/shyam-s00/gopher-glide"><img src="https://goreportcard.com/badge/github.com/shyam-s00/gopher-glide" alt="Go Report Card"></a>
  <a href="https://plugins.jetbrains.com/plugin/30983-gopher-glide"><img src="https://img.shields.io/jetbrains/plugin/v/dev.gopherglide.gg-plugin" alt="JetBrains Plugin Version"></a>
</p>

---

**Gopher-Glide (gg)** is an open-source, high-fidelity **API traffic simulator** built in Go. Designed to move beyond brute-force load testing, `gg` runs your standard IDE `.http` REST Client files right out of the box. 

Generate perfectly smooth, high-concurrency traffic using the lock-free Actor Model. Adjust RPS in real-time via the interactive TUI, catch API regressions using semantic snapshots (`gg snap`), and integrate natively into your CI/CD pipelines. No scripting, no boilerplate code.

👉 **[Read the Full Documentation](https://gopherglide.dev/)**

---

## Quick Start

### 1. macOS / Linux (Homebrew)
The easiest way to install is via Homebrew:
```bash
brew install shyam-s00/tap/gg
```

### 2. Docker
Perfect for CI/CD pipelines:
```bash
docker run --rm -v $(pwd):/workspace ghcr.io/shyam-s00/gopher-glide:latest config.yaml
```

### 3. Pre-built binary
Go to the [Releases](https://github.com/shyam-s00/gopher-glide/releases) page and download the archive for your platform. Extract the binary and place it in your `$PATH`.

---

## 🔌 JetBrains IDE Integration 

Gopher Glide features an official [JetBrains plugin](https://plugins.jetbrains.com/plugin/30983-gopher-glide) that brings **load testing** directly into your IDE. 
- Smart YAML editing for `config.yaml`
- Terminal-First Execution
- Snap UI Tool Window for exploring Snapshots

---

## 🚀 Performance

The Hive Engine is built for maximum throughput with zero garbage-collection penalties. 
- **Sequential Peak:** ~31,000 Requests Per Second (RPS) per core.
- **Parallel Aggregate:** ~89,000+ RPS total system throughput on standard 12-core hardware.
- **Zero Garbage:** The metrics subsystem operates with absolutely 0 memory allocations.

For full technical breakdown and latency details, see the **[Official Benchmarks](docs/BENCHMARKS.md)**.

---

## Planned Features
- [ ] **Chaos / Fault Injection** — Simulate poor network conditions (3G packet loss, latency jitter) to see how APIs handle degradation.
- [ ] **Distributed Simulation Mesh** — Run `gg worker` nodes across multiple servers/regions, controlled by a central `gg master` UI.
- [ ] **Auto-Pilot (Smart Scaling)** — Set a `--target-latency` and let the engine automatically ramp RPS to find your API's exact breaking point.


## License
[MIT](LICENSE)
