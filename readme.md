# gg — Gopher Glide

[![Build](https://github.com/shyam-s00/gopher-glide/actions/workflows/ci.yml/badge.svg)](https://github.com/shyam-s00/gopher-glide/actions/workflows/ci.yml)

A lightweight, terminal-based HTTP load testing tool built in Go. `gg` reads your requests from a standard `.http` file and hammers your endpoints at a target RPS, giving you a live dashboard of throughput, latency, and errors — all in your terminal.

---

## Features

- **`.http` file support** — define http requests (with headers and bodies) using the familiar `.http` / REST Client format
- **RPS-based load engine** — ticker-driven scheduler dispatches requests at a configured target RPS using goroutines
- **Concurrent execution** — powered by `errgroup` + channels; no heavy frameworks, minimal memory footprint
- **Live TUI dashboard** — real-time stats rendered with Bubble Tea & Lip Gloss:
  - Active VPUs (concurrent goroutines in flight)
  - Requests completed & error rate
  - Throughput (RPS)
  - Latency percentiles — avg, min, max, p50, p95, p99
  - Scrollable debug/call log panel with all/errors toggle
- **YAML configuration** — set `httpFile`, `duration`, `target_rps`, and circuit-breaker threshold in `config.yaml`
- **Stamped binaries** — version, git commit and build date embedded at compile time via `-ldflags`
- **Cross-platform** — pre-built binaries for Linux (amd64/arm64), macOS (amd64/arm64), and Windows (amd64)

---

## Quick Start — pre-built binary

### 1. Download the latest release

Go to the [Releases](https://github.com/shyam-s00/gopher-gun/releases) page and download the archive for your platform:

| Platform | Archive |
|---|---|
| macOS (Apple Silicon) | `gg-<version>-darwin-arm64.tar.gz` |
| macOS (Intel) | `gg-<version>-darwin-amd64.tar.gz` |
| Linux (x86-64) | `gg-<version>-linux-amd64.tar.gz` |
| Linux (ARM64) | `gg-<version>-linux-arm64.tar.gz` |
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

macOS Gatekeeper quarantines unsigned binaries downloaded from the internet. Remove the quarantine attribute before running:

```bash
xattr -dr com.apple.quarantine ./gg
```

### 4. Configure

Edit `config.yaml`:

```yaml
config:
  httpFile: "request.http"   # path to your .http file (same directory)
  prometheus: false
  breaker_threshold_pct: 20.0

stages:
  - duration: 30s
    target_rps: 50
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

### Build for the current platform (dev build)

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
dist/gg-linux-arm64
dist/gg-darwin-amd64
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
### Get Users
GET https://httpbin.org/get
Accept: application/json
X-Request-ID: gg-001

### Get with query param
GET https://httpbin.org/get?userId=1
Accept: application/json
Cache-Control: no-cache

### Create Post
POST https://httpbin.org/post
Content-Type: application/json
Accept: application/json

{
  "title": "hello",
  "userId": 1
}
```

- Supported methods: `GET`, `POST` (and any valid HTTP verb)
- Headers are placed directly after the request line
- Body (for POST/PUT/PATCH) follows a blank line after the headers
- `###` separates requests; the text after `###` is a label (ignored by the engine)

---

## Configuration reference

```yaml
config:
  httpFile: "request.http"        # .http file to load (relative to config.yaml)
  prometheus: false               # (planned) expose /metrics endpoint
  breaker_threshold_pct: 20.0     # (planned) circuit-breaker error-rate threshold %

stages:
  - duration: 30s                 # how long to run this stage
    target_rps: 50                # target requests per second
```

---

## TUI Dashboard

```
┌─────────────────────────────────────────────────────────────────────┐
│  gg — Gopher Glide                              v0.1.0  [Running]   │
├──────────────────────┬──────────────────────┬───────────────────────┤
│  Configuration       │  Throughput          │  Latency              │
│  Target RPS   50     │  RPS         48.2    │  Avg      142.3 ms    │
│  Duration     30s    │  Completed   1 446   │  Min       88.1 ms    │
│  Uptime       00:30  │  Errors         12   │  Max      310.5 ms    │
│  Active VPUs    18   │  Error Rate   0.8%   │  P50      138.0 ms    │
│                      │                      │  P95      278.4 ms    │
│                      │                      │  P99      305.2 ms    │
├──────────────────────┴──────────────────────┴───────────────────────┤
│  Debug Log  [All | Errors]                                          │
│  …scrollable call log…                                              │
└─────────────────────────────────────────────────────────────────────┘
```

Press `q` or `Ctrl+C` to quit.

---

## Project structure

```
.
├── cmd/gg/main.go              entry point
├── config.yaml                 default configuration
├── request.http                sample request file
├── internal/
│   ├── config/                 YAML config parser
│   ├── httpreader/             .http file parser
│   ├── engine/                 load engine (scheduler + worker pool)
│   ├── tui/                    Bubble Tea TUI
│   └── version/                build-time version info
└── Makefile
```

---

## Roadmap

- [ ] Multi-stage load profiles (ramp-up / sustained / ramp-down)
- [ ] VPU mode — fixed virtual users instead of fixed RPS
- [ ] Circuit breaker — auto-stop on the error-rate threshold
- [ ] HTML / JSON result export

---

## License

[MIT](LICENSE)

