---
hide:
  - navigation
---

# High-fidelity API traffic simulation from your IDE.

**Gopher-Glide (gg)** is an open-source, high-fidelity API traffic simulator. Move beyond brute-force load testing with native IDE integration, zero-config profiles, and an interactive TUI that lets you inject chaos in real-time.

[Get Started (Quickstart)](#installation-quick-start){ .md-button .md-button--primary }
[View on GitHub](https://github.com/shyam-s00/gopher-glide){ .md-button }

---

## Why Gopher-Glide?

<div class="grid cards" markdown>

-   :material-code-braces: **Code-to-Load Pipeline**
    
    ---
    Reuse your existing `.http` REST Client files directly. Featuring a native JetBrains IDE plugin, there is no need to rewrite requests in JavaScript or Python. Just point `gg` at your file and go.

-   :material-chart-bell-curve-cumulative: **Zero-Config Profiles**
    
    ---
    Skip writing complex YAML configs. Use built-in patterns like `--profile flash-sale` or `--profile ddos` to generate standard industry traffic shapes instantly.

-   :material-monitor-dashboard: **Interactive Chaos TUI**
    
    ---
    Adjust traffic in real-time with Director Mode. Bias RPS up or down using your arrow keys, and watch the beautiful terminal UI react instantly.

-   :material-camera-iris: **Semantic Snapshots (`gg snap`)**
    
    ---
    Record and view behavioral snapshots (latency, status distributions, and inferred JSON schemas) for semantic API diffing and regression testing.

-   :material-robot-outline: **CI/CD Ready (Headless Mode)**
    
    ---
    Run perfectly in CI pipelines using the built-in `--headless` mode. Combine with `gg snap assert` to act as an automated performance and regression gate.

-   :material-engine: **Coming Soon: The Hive Engine**
    
    ---
    The upcoming Hive Engine introduces a lock-free Actor Model. Soon, you'll be able to simulate organic, stateful user journeys with ultra-high RPS from a single instance.

</div>

---

## How does it compare?

Unlike traditional tools (**k6, Locust, JMeter**) that require you to translate your API requests into JavaScript, Python, or heavy XML configs, Gopher-Glide offers a **scriptless** experience. By natively supporting `.http` files and utilizing an Actor Model for traffic generation, you get the performance of a compiled Go binary with the developer experience of your favorite IDE.

---

## Features

- **Zero-Config Profiles** — skip writing complex YAML files. Use built-in patterns like `--profile flash-sale` or `--profile ddos` to generate standard industry traffic shapes.
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
