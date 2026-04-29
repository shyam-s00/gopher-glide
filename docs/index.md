# Why Gopher Glide? (vs k6, Locust, JMeter)

Developers frequently look for a **lightweight alternative** to traditional tools. Most load testers require you to translate your API requests into JavaScript, Python, or heavy XML configs. 

**Gopher Glide** offers a **scriptless load testing** experience:

1. **Zero-Scripting:** Reuse your existing VS Code or JetBrains IDE `.http` REST Client files directly.
2. **Behavioral Profiling:** Not just metrics, `gg` captures payload sizes and infers JSON schemas for **API schema regression testing** and **semantic API diffing**.
3. **Interactive Load Generation:** The live Director Mode allows you to bias RPS up or down in real-time.
4. **CI/CD Ready:** Use the built-in `--headless` mode and `gg snap assert` command to act as an automated performance and regression gate in your test pipelines.

---

## Features

- **Native `.http` file support** — define requests (with headers and bodies) using the familiar REST Client format; point `gg` at your existing file and go
- **Multi-stage load engine** — define any number of stages; the engine linearly interpolates (LERP) RPS between stages automatically
  - **Ramp Up** — smoothly increase the load to a target RPS
  - **Sustain** — hold a fixed RPS for a duration
  - **Spike** — instant step jump (`duration: 0s`) with no interpolation
  - **Ramp Down** — smoothly reduce the load back to zero (cool-down)
  - **Named stages** — optional `name:` field used in the TUI timeline label
- **RPS-based scheduler** — drift-free ticker dispatches requests at the configured rate; never accumulates lag across second boundaries
- **Concurrent worker pool** — powered by `errgroup` + channels; worker count scales to peak RPS across all stages; minimal memory footprint
- **Jitter** — configurable `±N%` organic noise on the RPS ticker so load patterns look realistic rather than mechanical
- **Timescale** — `time_scale` compresses or stretches the stage clock for fast local iteration (e.g. `time_scale: 10` runs a 10-minute plan in 60 seconds)
- **Director Mode** — live RPS bias while a run is in progress:
  - `↑` / `↓` keys adjust the running RPS by ±5 in real-time
  - Bias is applied on top of the LERP'd stage target and shown in the TUI
- **Live TUI dashboard** — rendered with Bubble Tea & Lip Gloss
- **Semantic Snapshots (`gg snap`)** — record and view behavioral snapshots (latency, status distribution, and inferred JSON schemas) for **semantic API diffing** and regression testing.
- **Stamped binaries** — version, git commit, and build date embedded at compile time via `-ldflags`
- **Cross-platform** — pre-built binaries for Linux (amd64), macOS (arm64), and Windows (amd64)
- **JetBrains Plugin** — a dedicated IDE plugin is available for integrating Gopher Glide runs into your workflow

---

## Installation & Quick Start

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
