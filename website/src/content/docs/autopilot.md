---
title: "Autopilot"
description: "Let gg find your API's capacity ceiling or defend an SLO on its own, then bring in an AI agent for planning and analysis. Built-in controller, gg autopilot agents, and an ACP agent for JetBrains and Zed."
---

Autopilot is how `gg` runs a load test **for** you. It comes in two layers, and you can use either one alone:

| Layer | What it does | Cost | Reacts in |
|---|---|---|---|
| **[Built-in controller](#the-built-in-controller)** | Steps the rate, watches health, backs off or clamps. Plain Go, no model involved. | Free | About a second |
| **[AI agents (`gg autopilot`)](#ai-agents-gg-autopilot)** | Writes and verifies `.http` files, plans a mission from a sentence, reads snapshots and explains what changed. | Your model's tokens | Seconds |

The split is deliberate. Deciding "p99 just crossed 200 ms, drop the rate" has to happen every second and should cost nothing, so a deterministic controller does it. Deciding *what to test* and *what the result means* is where a language model earns its keep, so the agent does that and hands the second-by-second work to the controller.

> **This sends real traffic**, like every `gg` run. Point autopilot at services you own. The agent layer is bound by the same [safety caps](/mcp#safety) as `gg mcp`.

---

## The built-in controller

Two modes, started with one flag. Both judge health on a **window**: the last few seconds of traffic since the last rate change, with a minimum number of samples. They never look at the cumulative p99 or error rate, which cannot show the effect of a five-second step or a recovery.

### `find-ceiling`: how much can it take?

```bash
gg --http-file api.http --autopilot find-ceiling --max-p99 200ms --max-rps 500 --snap
```

`gg` pins the rate at a start value, raises it a step at a time, and stops when your limits break:

1. **Step.** Holds `--start-rps` (default 20), then raises the rate by `--step-pct` (default 25%) every `--step-interval` (default 5 s). Each step is judged only on the window after that step.
2. **Breach.** When p99 passes `--max-p99` (default 250 ms) or the error rate passes `--max-error-rate` (default 1%), gg drops a **mark** on the timeline saying what broke and at which rate, and, when errors were the cause, which endpoint was failing most.
3. **Back off, then confirm.** It returns to the last healthy rate and holds it for `--confirm-hold` (default 15 s) to prove it is sustainable. If that fails too, it steps down again.
4. **Report.** The run ends on its own with one of these outcomes, recorded in the snapshot.

| Outcome | Meaning |
|---|---|
| `ceiling_found` | The reported rate held through the confirmation window. |
| `ceiling_not_reached` | The target never degraded before `--max-rps`; the cap is as far as gg went. |
| `unstable` | No rate could be confirmed within `--duration`, or a breach left nowhere lower to step. |
| `stopped` | You stopped the run first. |

Here is the headless output of a real run, abridged, against a local service that slows down past about 120 requests per second (`--start-rps 30 --step-interval 3s --max-p99 200ms --confirm-hold 6s`). The whole search took 37 seconds:

```text
[2026-11-17T01:47:28Z] autopilot stepping: starting at 30 RPS
[2026-11-17T01:47:31Z] autopilot stepping: healthy at 30 RPS (p99=10ms, errors=0.00%); stepping to 38
[2026-11-17T01:47:34Z] autopilot stepping: healthy at 38 RPS (p99=10ms, errors=0.00%); stepping to 48
...
[2026-11-17T01:47:52Z] autopilot stepping: healthy at 118 RPS (p99=10ms, errors=0.00%); stepping to 148
[2026-11-17T01:47:56Z] mark "Autopilot: ceiling breached at 148 RPS (p99=401ms > 200ms); backing off to 118 RPS" (t+28.0s)
[2026-11-17T01:47:56Z] autopilot confirming: confirming 118 RPS for 6s
[2026-11-17T01:48:03Z] mark "Autopilot: ceiling confirmed at 118 RPS" (t+35.0s)
[2026-11-17T01:48:03Z] autopilot done: ceiling confirmed at 118 RPS
```

`find-ceiling` owns the schedule, so it cannot be combined with `--profile` (that is a startup error), and any `stages` in a config file are ignored. `--duration` is how long it may search (default 5 minutes), and `--max-rps` (default 1000) is a hard cap on what it will try.

### `slo-guard`: hold the line during a known traffic shape

```bash
gg --http-file api.http --profile flash-sale --autopilot slo-guard --max-p99 250ms --snap
```

The profile runs as planned. If health breaches on two consecutive ticks, the guard **clamps** the rate to a safe level. Once health stays inside an 80% margin for `--recover-hold` (default 10 s), it releases the clamp in steps rather than all at once. After three clamp cycles it holds the recovered rate for the rest of the run, so a flapping service cannot make it oscillate. It never changes the run's length.

### Flags

| Flag | Default | Applies to | Description |
|---|---|---|---|
| `--autopilot <mode>` | off | both | `find-ceiling` or `slo-guard`. |
| `--max-p99 <dur>` | `250ms` | both | p99 latency limit. |
| `--max-error-rate <0-1>` | `0.01` | both | Error-rate limit as a fraction. |
| `--min-samples <n>` | `50` | both | Latency samples a window needs before it is judged. |
| `--step-interval <dur>` | `5s` | both | Time between rate steps. |
| `--max-rps <n>` | `1000` | `find-ceiling` | Never exceed this rate. |
| `--start-rps <n>` | `20` | `find-ceiling` | First rate to hold. |
| `--step-pct <n>` | `25` | `find-ceiling` | Percent to raise the rate each step. |
| `--confirm-hold <dur>` | `15s` | `find-ceiling` | How long the backed-off rate must stay healthy. |
| `--recover-hold <dur>` | `10s` | `slo-guard` | How long health must stay in margin before releasing. |
| `--duration <dur>` | `5m` | `find-ceiling` | Longest the search may run. |

In the [TUI](/tui) the arrow keys are ignored while the controller owns the rate, and its state shows in the director bar. In headless mode it emits `autopilot` events and a compact status on every heartbeat; see the [Live Control Protocol](/control-protocol#autopilot).

---

## Mission reports

Every run that uses `--snap` can leave behind a report you can paste into a pull request or send to a colleague:

```bash
gg --http-file api.http --autopilot find-ceiling --snap --report mission.md
gg snap report my-tag --format html --out mission.html
```

The report has the headline numbers, a timeline that merges marks, rate changes, holds and autopilot decisions (each badged by who made it: `user`, `agent` or `autopilot`), the top failing endpoints, and the autopilot result. Markdown is sized for a PR comment. HTML is one self-contained file with an inline chart and no external requests. See [`gg snap report`](/snap#mission-reports).

---

## AI agents (`gg autopilot`)

`gg autopilot` runs three agents that drive gg through the same [MCP tools](/mcp-tools) any other agent would use, so they inherit its [safety caps](/mcp#safety). Each one is a short task:

```bash
gg autopilot generate "a bookstore API at http://localhost:8080 with login, search and checkout"
gg autopilot run "find how many checkouts per second we can take with p99 under 300 ms"
gg autopilot triage "why is the latest run slower than the baseline?"
```

| Mode | What it does |
|---|---|
| `generate` | Writes a `.http` file from a description, a route list or an OpenAPI file, validates it, dry-runs it against the real service and fixes whatever fails, for up to `--max-turns` attempts (default 4). It saves the file only after it passes validation, to `--out` (default `requests.http`). |
| `run` | Turns a goal into a monitored simulation: picks `find-ceiling`, `slo-guard` or a profile, watches live telemetry, marks what it does, stops the run and summarises with the report path. |
| `triage` | Read-only. Lists snapshots, compares them, runs the same pass/fail check as `gg snap assert`, and ranks the likely root causes, saying so when manual steering confounds the comparison. |

The agent starts the built-in controller for anything that needs second-by-second reaction, so you pay for planning and analysis, not for every tick.

### Pick a model

| Provider | How |
|---|---|
| **Gemini** | Set `GEMINI_API_KEY` (or `GOOGLE_API_KEY`). The default model is `gemini-flash-latest`; override it with `--model` or `GG_AUTOPILOT_MODEL`. |
| **Vertex AI** | Set `GOOGLE_GENAI_USE_VERTEXAI=true` with your usual Google Cloud environment. |
| **Ollama, vLLM, LM Studio, or any OpenAI-compatible server** | `--base-url http://localhost:11434/v1 --model qwen3` (or `GG_AUTOPILOT_BASE_URL`). Set `GG_AUTOPILOT_API_KEY` if the server wants a key. |

```bash
# fully local, nothing leaves your machine
gg autopilot generate --base-url http://localhost:11434/v1 --model qwen3 "an items API at http://localhost:8080"
```

A model that supports tool calling is required. Small local models vary a lot in how reliably they call tools, so if one wanders, try a larger one before debugging gg.

### Where the tools come from

By default `gg autopilot` starts an in-process gg server, bound by `--max-rps`, `--max-duration` and `--allow-host` (the same caps as [`gg mcp`](/mcp#safety)). To share runs with an already-running daemon, for example the one your IDE plugin manages, point it there:

```bash
gg autopilot run --mcp-url http://127.0.0.1:8765/mcp --auth-token "$GG_MCP_TOKEN" "find the ceiling"
# or: export GG_MCP_URL=http://127.0.0.1:8765/mcp
```

Anything the agent starts is stopped if you cancel or it exits, whether the server is in-process or a daemon.

---

## In your IDE: the ACP agent

`gg autopilot --acp` serves the [Agent Client Protocol](https://agentclientprotocol.com) (v1) over stdio, so an ACP-capable client can list **gg autopilot** as an agent. It streams its reasoning, a step plan, a card for every tool call and the final answer.

### Slash commands that need no API key

The agent works out of the box even before you configure a model, because these commands are deterministic and call gg directly:

| Command | What it does |
|---|---|
| `/dry-run api.http` | Runs each journey once and shows every step. |
| `/find-ceiling api.http max_p99=200 max_rps=300` | Runs the built-in ceiling search and reports the result and report path. Also takes `max_error_rate`, `start_rps`, `duration`. |
| `/slo-guard api.http profile=load peak_rps=200` | Runs a profile under the guard. Also takes `duration`, `max_p99`, `max_error_rate`. |
| `/triage [baseline] [current]` | Compares the two newest snapshots, or the two you name. Offered once at least two snapshots exist. |
| `/generate <description>` | Writes and verifies a `.http` file. Offered only when a model is configured. |

Relative paths resolve against the project folder. Anything that is not a slash command goes to the model-backed agent; without a model it says so and lists the commands above. Cancelling a turn stops any run it started.

![JetBrains Air's new session box listing the gg autopilot slash commands: dry-run, find-ceiling, slo-guard and triage](/assets/plugin-acp-runs.png)

### Set it up

```bash
gg mcp init --ide jetbrains --register-acp      # JetBrains IDEs
```

`gg mcp init` writes the workspace's `.mcp.json`. `--register-acp` also adds the agent to the **user-wide** `~/.jetbrains/acp.json` (`%USERPROFILE%\.jetbrains\acp.json` on Windows), because that is where JetBrains reads custom ACP agents. It is opt-in for that reason: the file is shared by every project. It keeps every other entry and unknown key exactly as they were, refuses to touch a file that is not valid JSON, and `--print` shows what it would write. **Restart the IDE** afterwards.

Prefer to do it by hand? Add an entry to `acp.json` (the `command` must be an absolute path):

```json
{
  "agent_servers": {
    "gg autopilot": {
      "command": "/usr/local/bin/gg",
      "args": ["autopilot", "--acp"],
      "use_custom_mcp": true
    }
  }
}
```

The JetBrains plugin can do all of this for you: **Tools → Connect AI Agents to Gopher Glide…** registers the agent through a small launcher that connects it to the IDE's shared daemon, so the runs it starts show live in the plugin's Run tab. See [IDE Plugins](/plugin).

For Zed, add the same command and args under `agent_servers` in its settings. Set `GEMINI_API_KEY` (or the `GG_AUTOPILOT_*` variables) in the environment the IDE starts the agent with to enable open-ended requests.

See also: [AI Agents (MCP)](/mcp), [MCP Tool Reference](/mcp-tools), [CLI Reference](/cli-reference#gg-autopilot-ai-agents), [Snapshots](/snap).
