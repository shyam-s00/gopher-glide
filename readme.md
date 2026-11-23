<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="website/public/assets/ggToolIcon_dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="website/public/assets/ggToolIcon.svg">
    <img alt="Gopher Glide Logo" src="website/public/assets/ggToolIcon.svg" width="250" />
  </picture>
</div>

<h1 align="center">Gopher Glide (gg) 🚀</h1>
<p align="center"><b>Unleash The Hive. Destroy your servers, not your RAM.</b></p>
<p align="center">Load-test any HTTP API from the <code>.http</code> files you already have. Let the built-in autopilot find its breaking point, or hand the whole loop to an AI agent.</p>

<p align="center">
  <a href="https://github.com/shyam-s00/gopher-glide/actions/workflows/ci.yml"><img src="https://github.com/shyam-s00/gopher-glide/actions/workflows/ci.yml/badge.svg" alt="Build"></a>
  <a href="https://github.com/shyam-s00/gopher-glide/releases/latest"><img src="https://img.shields.io/github/v/release/shyam-s00/gopher-glide?sort=semver&label=release" alt="Release"></a>
  <a href="https://codecov.io/gh/shyam-s00/gopher-glide"><img src="https://codecov.io/gh/shyam-s00/gopher-glide/graph/badge.svg" alt="codecov"></a>
  <a href="https://goreportcard.com/report/github.com/shyam-s00/gopher-glide"><img src="https://goreportcard.com/badge/github.com/shyam-s00/gopher-glide" alt="Go Report Card"></a>
  <a href="https://plugins.jetbrains.com/plugin/30983-gopher-glide"><img src="https://img.shields.io/jetbrains/plugin/v/dev.gopherglide.gg-plugin" alt="JetBrains Plugin Version"></a>
  <a href="https://marketplace.visualstudio.com/items?itemName=gopherglide.gg-plugin"><img src="https://img.shields.io/github/v/release/shyam-s00/gg-vscode-plugin?label=VS%20Code%20Plugin" alt="VS Code Plugin Version"></a>
  <a href="https://open-vsx.org/extension/gopherglide/gg-plugin"><img src="https://img.shields.io/open-vsx/v/gopherglide/gg-plugin?label=Open%20VSX" alt="Open VSX Version"></a>
</p>
<p align="center">
  <img src="website/public/assets/demo.gif" alt="Gopher Glide TUI Demo" width="800" />
</p>

---

## 🤷‍♂️ Why not just use wrk, k6, or JMeter?

If you want to run a quick concurrency test on an endpoint, you usually have to write a custom JavaScript file for `k6`, a Python script for `Locust`, or learn a heavy configuration language. 

**Gopher Glide (gg)** is a modern, open-source API traffic simulator that lets you skip the boilerplate. It directly executes standard IDE `.http` REST Client files that you probably already have sitting in your workspace. 

| Feature | Gopher Glide (`gg`) | k6 / Locust | wrk / hey |
| :--- | :--- | :--- | :--- |
| **Scripting** | **None** (Reads `.http` natively) | JavaScript / Python | None (CLI flags only) |
| **Traffic Control** | **Live Interactive TUI** | Requires configs | Fixed concurrency only |
| **Finding the ceiling** | **Built-in autopilot** (`find-ceiling`, `slo-guard`) | Hand-written ramp scripts | Manual reruns |
| **AI agents** | **MCP server + ACP agent**, with hard safety caps | Not built in | Not built in |
| **CI/CD Assertions** | **Semantic JSON Diffing** | Pass/Fail Thresholds | Raw latencies only |
| **Performance** | **30k+ RPS** | Medium-High | Extremely High |

👉 **[Read the Full Documentation at gopherglide.dev](https://gopherglide.dev/)**

---

## ⚡ 30-Second Quickstart

### 1. Install
```bash
brew install shyam-s00/tap/gg
```
Or download a prebuilt binary for macOS, Linux or Windows from [GitHub Releases](https://github.com/shyam-s00/gopher-glide/releases/latest), or use Docker: `docker pull ghcr.io/shyam-s00/gopher-glide:latest`.

### 2. Point it at any `.http` file
Create a simple `api.http` file (or use an existing one from your IDE):
```http
GET http://localhost:8080/health
Accept: application/json
```

### 3. Run the simulation
Use a built-in profile to automatically shape the traffic (e.g., ramping up, sustaining, and cooling down):
```bash
gg --profile flash-sale --http-file api.http
```

You drop straight into the interactive TUI, where you can watch latencies and adjust the load in real time.

### 4. Or let the autopilot find the limit
Not sure how much traffic your service can take? `gg` steps the load up, backs off when your limits break, and reports the highest rate that held:
```bash
gg --http-file api.http --autopilot find-ceiling --max-p99 200ms --max-rps 300 --snap
```
> `gg` sends real traffic. Point it at services you own.

---

## 🛠 Feature Deep-Dives

### 🐝 The Hive Engine (Zero-Allocation Architecture)
The core of `gg` is built on a pure-Go lock-free Actor Model. By isolating connections into ultra-lightweight Goroutines and tracking metrics via sharded lock-free atomics, `gg` operates at **`0 allocs/op`** on the hot path. This means you can comfortably push over **30,000+ RPS** on a standard developer machine without garbage-collection pauses destroying your latency percentiles.

> **⚔️ Benchmark:** At 30,000 RPS target load, `gg` consumes **40% less memory** (1.42 GB vs 2.38 GB) and delivers **3x more successful requests** than `k6`. `gg`'s dynamic Adaptive Backpressure acts as an intelligent edge proxy—gracefully throttling excess traffic when the target server breaks to ensure maximum goodput. [Read the full benchmark breakdown](https://gopherglide.dev/benchmarks) to see how `gg` safely simulates massive traffic.

### 🎯 Built-in Autopilot (`--autopilot`)
Autopilot runs the load test **for** you, with plain Go and no model involved, so it costs nothing and reacts within about a second. It judges health on a fresh window of traffic after every step, never on run-long averages.

- **`find-ceiling`** steps the rate up, marks the breach on the timeline (what broke, at which rate, and which endpoint was failing), backs off to the last healthy rate and confirms it held. Outcomes: `ceiling_found`, `ceiling_not_reached`, `unstable`, `stopped`.
- **`slo-guard`** runs your traffic profile as planned and clamps the rate if the SLO breaches, then releases the clamp in steps once health recovers.

Every run can end with a shareable **mission report**: headline numbers, a timeline of marks and rate changes (badged `user`, `agent` or `autopilot`), the top failing endpoints and the autopilot result. Pass `--report mission.html` for one self-contained HTML file, or use `.md` for a Markdown report sized for a PR comment. You can also build one later with `gg snap report <tag>`. See the [autopilot guide](https://gopherglide.dev/autopilot).

### 🔬 X-Ray Vision for APIs (`gg snap`)
Know *exactly* why you broke production, not just when. By passing the `--snap` flag, Gopher Glide infers your API's JSON schema in real-time. You can then use `gg snap diff` and `gg snap assert` in your CI/CD pipeline to compare two snapshots and spot missing fields, type changes, or massive payload bloat. Headless runs (`--headless --reporter json`) emit machine-readable heartbeats for CI and tooling.

### 🔌 IDE Integration
Gopher Glide has official plugins for **[JetBrains](https://plugins.jetbrains.com/plugin/30983-gopher-glide)** (IntelliJ, GoLand, WebStorm, Rider, and more) and **[VS Code](https://marketplace.visualstudio.com/items?itemName=gopherglide.gg-plugin)** (also available on [Open VSX](https://open-vsx.org/extension/gopherglide/gg-plugin) for VSCodium, Gitpod, and other compatible editors). Both plugins bring the full `gg` workflow into your editor — run profiles directly from any `.http` file, watch a live RPS/latency dashboard, and explore semantic snapshot diffs, all without touching a terminal. Both can also steer a run live — nudge the target RPS, drop named marks, and stop cleanly with the snapshot intact (requires `gg` v1.3+).

### 🤖 AI Agents (MCP and ACP)
`gg mcp` serves gg to AI agents over the Model Context Protocol. An agent in Claude Code, Cursor, VS Code or a JetBrains IDE can validate and dry-run a `.http` file, start a load test, read live telemetry, steer the rate, drop marks, stop, and diff the result against a baseline snapshot, through **11 tools**, all within caps you set (`--max-rps`, `--max-duration`, `--allow-host`). The agent can hand second-by-second steering to the built-in autopilot instead of spending tokens on it.

```bash
gg mcp init                 # writes the MCP config, a Claude skill and three Copilot agents for your project
claude mcp add gg -- gg mcp # or wire up Claude Code by hand
```

`gg autopilot` adds agents of its own: `generate` writes and verifies a `.http` file from a description, `run` plans a mission from a sentence, and `triage` explains what changed between two snapshots. They use Gemini (`GEMINI_API_KEY`) or a local OpenAI-compatible model such as Ollama. `gg autopilot --acp` serves the same agent over the Agent Client Protocol for ACP-capable editors, and its slash commands (`/dry-run`, `/find-ceiling`, `/slo-guard`) work without any API key.

See the [MCP guide](https://gopherglide.dev/mcp) and the [autopilot guide](https://gopherglide.dev/autopilot).

### 🔄 Stateful Journeys & Variable Extraction
No more writing custom JavaScript to test multi-step workflows. `gg` natively understands JetBrains HTTP variable extraction syntax. You can authenticate, extract a token using `@gg-export`, and instantly inject it into the next request using `{{token}}`.

---

## Planned Features
- [ ] **Chaos / Fault Injection** — Simulate poor network conditions (3G packet loss, latency jitter) to see how APIs handle degradation.
- [ ] **Distributed Simulation Mesh** — Run `gg worker` nodes across multiple servers/regions, controlled by a central `gg master` UI.

## License
[MIT](LICENSE)
