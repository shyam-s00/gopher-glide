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

## Roadmap
- [x] Snap – Behavioral Profiling
- [ ] VPU mode — fixed virtual users instead of fixed RPS
- [ ] Circuit breaker — auto-stop on a configurable error-rate threshold
- [ ] Prometheus `/metrics` endpoint
- [ ] HTML / JSON result export

## License
[MIT](LICENSE)
