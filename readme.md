# Gopher Glide (gg) — High-Performance API Load Testing Tool Built in Go 🚀

[![Build](https://github.com/shyam-s00/gopher-glide/actions/workflows/ci.yml/badge.svg)](https://github.com/shyam-s00/gopher-glide/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/shyam-s00/gopher-glide?sort=semver&label=release)](https://github.com/shyam-s00/gopher-glide/releases/latest)
[![codecov](https://codecov.io/gh/shyam-s00/gopher-glide/graph/badge.svg)](https://codecov.io/gh/shyam-s00/gopher-glide)
[![Go Report Card](https://goreportcard.com/badge/github.com/shyam-s00/gopher-glide)](https://goreportcard.com/report/github.com/shyam-s00/gopher-glide)
[![JetBrains Plugin Version](https://img.shields.io/jetbrains/plugin/v/dev.gopherglide.gg-plugin)](https://plugins.jetbrains.com/plugin/30983-gopher-glide)

**Gopher Glide (gg)** is an open-source, lightweight **CLI load testing tool** and **API benchmarking** utility built in Go. It simplifies performance testing by reading your requests from a standard `.http` file, running them through a multi-stage load plan, and delivering a live terminal dashboard of throughput, latency, and errors. 

Generate high-concurrency traffic with minimal overhead — no agents, no servers, no config sprawl.

---

## Features

- **`.http` file support** — define requests (with headers and bodies) using the familiar `.http` / REST Client format; point `gg` at your existing file and go
- **Multi-stage load engine** — define any number of stages; the engine linearly interpolates (LERP) RPS between stages automatically
  - **Ramp Up** — smoothly increase load to a target RPS
  - **Sustain** — hold a fixed RPS for a duration
  - **Spike** — instant step jump (`duration: 0s`) with no interpolation
  - **Ramp Down** — smoothly reduce load back to zero (cool-down)
  - **Named stages** — optional `name:` field used in the TUI timeline label
- **RPS-based scheduler** — drift-free ticker dispatches requests at the configured rate; never accumulates lag across second boundaries
- **Concurrent worker pool** — powered by `errgroup` + channels; worker count scales to peak RPS across all stages; minimal memory footprint
- **Jitter** — configurable `±N%` organic noise on the RPS ticker so load patterns look realistic rather than mechanical
- **Time scale** — `time_scale` compresses or stretches the stage clock for fast local iteration (e.g. `time_scale: 10` runs a 10-minute plan in 60 seconds)
- **Director Mode** — live RPS bias while a run is in progress:
  - `↑` / `↓` keys adjust the running RPS by ±5 in real-time
  - Bias is applied on top of the LERP'd stage target and shown in the TUI
- **Live TUI dashboard** — rendered with Bubble Tea & Lip Gloss:
  - Status header — version, run state (Running / Stopped), uptime
  - Three stat panels — Configuration, Throughput, Latency
  - Stage timeline graph — visual representation of all stages with a live cursor showing current position and achieved RPS marker per block
  - Scrollable call log — toggle between all calls and errors only with `f`
- **Snapshots (`gg snap`)** — record and view behavioral snapshots (latency, status distribution, and inferred JSON schemas) for all endpoints hit during a run.
- **Stamped binaries** — version, git commit, and build date embedded at compile time via `-ldflags`
- **Cross-platform** — pre-built binaries for Linux (amd64), macOS (arm64), and Windows (amd64)
- **JetBrains Plugin** — a dedicated IDE plugin is available in beta for integrating Gopher Glide runs into your workflow

---
## 🔌 JetBrains IDE Integration 

Gopher Glide features an official [JetBrains plugin](https://plugins.jetbrains.com/plugin/30983-gopher-glide) that brings **load testing** directly into your IDE. The plugin bridges the gap between your workspace and the TUI-based CLI, providing:

* **Smart YAML editing** — auto-complete, validation, and JSON Schema integration for your `config.yaml` load plans.
* **Clickable File References** — jump instantly from your config to your `.http` files.
* **Terminal-First Execution** — execute your API benchmarking directly into the IDE’s built-in tool window, complete with full TUI support.

## Quick Start — pre-built binary

### 1. Download the latest release

Go to the [Releases](https://github.com/shyam-s00/gopher-glide/releases) page and download the archive for your platform:

| Platform | Archive |
|---|---|
| macOS (Apple Silicon) | `gg-<version>-darwin-arm64.tar.gz` |
| Linux (x86-64) | `gg-<version>-linux-amd64.tar.gz` |
| Windows (x86-64) | `gg-<version>-windows-amd64.zip` |

### 2. Extract

```bash
# macOS / Linux
tar -xzf gg-<version>-darwin-arm64.tar.gz
cd gg-<version>-darwin-arm64
```

Each archive contains:
```
gg              ← the binary
config.yaml     ← configuration file
request.http    ← sample HTTP request file
```

### 3. macOS — remove quarantine (first run only)

macOS Gatekeeper quarantines unsigned binaries downloaded from the internet:

```bash
xattr -dr com.apple.quarantine ./gg
```

### 4. Configure

Edit `config.yaml`:

```yaml
config:
  httpFile: "request.http"        # .http file to load (same directory as config.yaml)
  jitter: 0.1                     # ±10% organic noise on the RPS ticker (0 = off)
  time_scale: 1.0                 # 1.0 = real-time; 2.0 = run 2× faster

stages:
  - duration: 10s
    target_rps: 50    # ramp 0 → 50 RPS over 10s

  - duration: 30s
    target_rps: 50    # sustain at 50 RPS for 30s

  - duration: 10s
    target_rps: 0     # ramp down to 0 (cool-down)
```

Edit `request.http` with your target endpoints (see [.http file format](#http-file-format) below).

### 5. Run

```bash
./gg config.yaml
```

---

## Build from source

### Prerequisites

- Go 1.21+
- `make`
- `git`

### Clone

```bash
git clone https://github.com/shyam-s00/gopher-glide.git
cd gopher-glide
```

### Build for the current platform

```bash
make build
```

Produces a `./gg` binary in the project root.

### Run directly

```bash
make run
```

Builds and immediately runs with `config.yaml`.

### Cross-compile all platforms

```bash
make build-all
```

Outputs binaries to `dist/`:

```
dist/gg-linux-amd64
dist/gg-darwin-arm64
dist/gg-windows-amd64.exe
```

### Package release archives

```bash
make release
```

Produces versioned `.tar.gz` (Unix) and `.zip` (Windows) archives in `dist/`, each bundling the binary + `config.yaml` + `request.http`.

### Other make targets

| Target | Description |
|---|---|
| `make build` | Compile for current OS/ARCH |
| `make build-all` | Cross-compile all platforms into `dist/` |
| `make release` | `build-all` + package archives |
| `make run` | Build + run with `config.yaml` |
| `make clean` | Remove `dist/` and local binary |
| `make version` | Print version, git commit, and build date |

---

## .http file format

`gg` uses the standard `.http` / REST Client file format. Requests are separated by `###`.

```http
### Simple GET
GET https://httpbin.org/get
Accept: application/json
X-Request-ID: gg-001

### GET with query param
GET https://httpbin.org/get?userId=1
Accept: application/json
Cache-Control: no-cache

### POST with JSON body
POST https://httpbin.org/post
Content-Type: application/json
Accept: application/json

{
  "title": "hello",
  "userId": 1
}
```

- Supported methods: `GET`, `POST`, and any valid HTTP verb
- Headers are placed directly after the request line
- Body (for POST/PUT/PATCH) follows a blank line after the headers
- `###` separates requests; the text after `###` is an optional label
- All requests are dispatched in round-robin order across stages

---

## Configuration reference

```yaml
config:
  httpFile: "request.http"        # .http file to load (relative to config.yaml)
  jitter: 0.1                     # ±10% noise on the RPS ticker; 0 disables (default: 0)
  time_scale: 1.0                 # stage clock multiplier; 2.0 = run 2× faster (default: 1.0)
  prometheus: false               # (planned) expose /metrics endpoint
  breaker_threshold_pct: 20.0     # (planned) circuit-breaker error-rate threshold %

stages:
  - name: "Warm Up"               # optional; inferred from shape if omitted
    duration: 10s                 # how long this stage runs
    target_rps: 50                # RPS target at the end of this stage

  - duration: 30s
    target_rps: 50                # same as previous → Sustain

  - duration: 0s
    target_rps: 200               # duration 0 → instant Spike (no interpolation)

  - duration: 10s
    target_rps: 200               # hold after spike → Sustain

  - duration: 10s
    target_rps: 0                 # ramp down → cool-down
```

### Stage inference rules

When `name` is omitted, `gg` infers the display label from the stage shape:

| Shape | Inferred label |
|---|---|
| `target_rps` higher than previous | **Ramp Up** |
| `target_rps` same as previous | **Sustain** |
| `target_rps` lower than previous | **Ramp Down** |
| `duration: 0s` | **Spike** |
| `target_rps: 0` | **Ramp Down** |

### `jitter`

Adds symmetric `±N%` noise to the inter-request interval so traffic looks organic. For example `jitter: 0.1` varies each interval by ±10%. The average rate is unchanged.

### `time_scale`

Compresses the stage clock by a multiplier. Useful for local testing:

```yaml
time_scale: 10   # a 10-minute plan finishes in 1 minute
```

---

## TUI Dashboard

```
  gg — Gopher Glide  v0.1.0  ●  RUNNING  ⏱ 00:42

 ╭─ Configuration ──────╮ ╭─ Throughput ─────────╮ ╭─ Latency ────────────╮
 │  Target RPS    50    │ │  RPS         48.2    │ │  Avg      142.3 ms   │
 │  Duration      70s   │ │  Completed   1 446   │ │  Min       88.1 ms   │
 │  Uptime       00:42  │ │  Errors         12   │ │  Max      310.5 ms   │
 │  Active VPUs    18   │ │  Error Rate   0.8%   │ │  P50      138.0 ms   │
 │                      │ │  Jitter       ±10%   │ │  P95      278.4 ms   │
 │                      │ │                      │ │  P99      305.2 ms   │
 ╰──────────────────────╯ ╰──────────────────────╯ ╰──────────────────────╯

 ╭─ Stage Timeline ──────────────────────────────────────────────────────────╮
 │  500 ┤                      ▓▓▓▓▓▓▓▓▓                                    │
 │      │              ░░░░░░░░▓▓▓▓▓▓▓▓▓░░░░░░                              │
 │      │       ░░░░░░░░░░░░░░░                    ░░░░░░░░                  │
 │    0 ┤░░░░░░░                                           ░░░░░░░░░         │
 │      [1/5] Ramp Up  •  stage 0:10 / 0:10  •  total 0:42 / 1:10           │
 ╰───────────────────────────────────────────────────────────────────────────╯

 [↑] +5 rps  [↓] -5 rps  [f] logs (FAILURES ONLY)  [q] quit   BIAS +10 RPS

 ╭─ Call Log ────────────────────────────────────────────────────────────────╮
 │  …scrollable call log…                                                    │
 ╰───────────────────────────────────────────────────────────────────────────╯
```

### Keybindings

| Key | Action |
|---|---|
| `↑` | Increase live RPS by +5 (Director Mode bias) |
| `↓` | Decrease live RPS by -5 (Director Mode bias) |
| `f` | Toggle call log between all calls and errors only |
| `q` / `Ctrl+C` | Quit |

---

## Multi-stage load profiles

`gg` uses **implicit ramping** — stage defines a `target_rps` and `duration`. The engine automatically interpolates (LERP) from the previous stage's rate to the new target. You never specify the "ramp type" explicitly; it is inferred from the numbers.

```yaml
stages:
  # Ramp Up: 0 → 100 RPS over 30s
  - duration: 30s
    target_rps: 100

  # Sustain: hold 100 RPS for 1 minute
  - duration: 1m
    target_rps: 100

  # Spike: jump instantly to 500 RPS
  - duration: 0s
    target_rps: 500

  # Sustain spike for 15s
  - duration: 15s
    target_rps: 500

  # Ramp Down: 500 → 0 RPS over 30s
  - duration: 30s
    target_rps: 0
```

The TUI timeline visualises the entire plan before and during the run, with a live cursor showing current position and an RPS marker per block showing the actual throughput achieved.

---

## Director Mode

While a run is in progress, use `↑` / `↓` to apply a live **RPS bias** on top of the configured stage target. The bias accumulates (e.g. three `↑` presses = +15 RPS) and is shown in the TUI and reflected in `GetMetrics()`. The bias is applied on top of the LERP'd value — the stage plan continues to run unaffected.

---

## Snapshots

`gg snap` is a tool suite for capturing, listing, and viewing test behavior records. It records detailed post-run telemetry per-endpoint without the overhead of heavy logging.

### Capture a Snapshot

To take a snapshot after the run, pass the `--snap` flag. You can also optionally tag your snapshot using `--snap-tag <tag>` to identify specific changes.

```bash
# Capture and tag the snapshot
gg config.yaml --snap --snap-tag "v1-baseline"
```

A `.snap` file is written to your system's default snapshot directory (or a custom one defined by `--snap-dir <dir>`).

### List Snapshots

List all saved snapshots with `gg snap list`:

```bash
gg snap list
```

This presents a summary table with columns such as Date, Tag, Endpoint Count, Peak RPS, and Total Requests.

### View a Snapshot

To view the detailed snapshot (status distribution, latency per endpoint, and inferred JSON schema), use `gg snap view`:

```bash
# View by exact Tag
gg snap view v1-baseline
```

It opens a rich TUI visualizing the endpoint data alongside schemas for the request payloads, giving you insights into status distributions, errors, and what the body contained.

### Snap Configuration Limits

In `config.yaml`, you can also configure options for the schema inference behavior:
```yaml
snap:
  sample_rate: 0.05       # 5% of responses analyzed for schema building
  max_samples: 200        # Store up to 200 JSON body samples per-endpoint
  max_body_kb: 500        # Byte limit (500 KB) for total samples stored per-endpoint
```
All snap values can also be overridden by command-line flags (e.g. `--snap-sample 0.1`).

---

## Project structure

```
.
├── cmd/gg/main.go              entry point
├── config.yaml                 default configuration
├── request.http                sample request file
├── internal/
│   ├── config/                 YAML config parser + validation
│   ├── httpreader/             .http file parser
│   ├── engine/                 load engine — LERP scheduler + worker pool + metrics
│   ├── tui/                    Bubble Tea TUI — dashboard, timeline, log panel
│   ├── snap/                   Snapshot logic: capturing, formatting, schema inferring
│   └── version/                build-time version info
└── Makefile
```

---

## Roadmap

- [ ] VPU mode — fixed virtual users instead of fixed RPS
- [ ] Circuit breaker — auto-stop on configurable error-rate threshold
- [ ] Prometheus `/metrics` endpoint
- [ ] HTML / JSON result export

---

## License

[MIT](LICENSE)
