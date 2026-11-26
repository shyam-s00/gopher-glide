---
title: "AI Agents (MCP)"
description: "Let an AI agent write, check, run, steer and judge load tests with gg, over the Model Context Protocol, with hard safety caps. One command sets up Claude Code, Cursor, VS Code and JetBrains."
---

`gg mcp` turns gg into a [Model Context Protocol](https://modelcontextprotocol.io) server. An agent in Claude Code, Cursor, VS Code or a JetBrains IDE can then do the whole loop on its own: **validate** a `.http` file, **dry-run** it against the real service, **start** a load test, **read live telemetry**, **steer** the traffic and **drop marks**, **stop**, and **diff the result against a baseline snapshot**.

Available from gg v1.5. Everything the agent does goes through the same engine, [Live Control Protocol](/control-protocol) and [snapshots](/snap) you already use, and every run is bounded by caps you set when you start the server.

**Want the agent to plan and explain, but not babysit?** Agents can hand the second-by-second steering to gg's built-in [autopilot](/autopilot), which reacts to a breach within a second and costs no tokens. Or skip the setup and use [`gg autopilot`](/autopilot#ai-agents-gg-autopilot), which ships three ready-made agents.

> **This sends real traffic.** `start_simulation`, `steer_traffic` and `run_journey_dry_run` hit whatever the `.http` file points at, including `POST`/`PUT`/`DELETE`. Point agents at services you own, and read [Safety](#safety) before exposing the server to anything but your own machine.

---

## Install

You need `gg` v1.5 or newer on your `PATH` (see [Getting Started](/getting-started)). The server speaks MCP over **stdio** by default, so the client starts it for you.

### One command

```bash
gg mcp init                      # everything below, for the project in the current directory
gg mcp init --ide claude         # or just one of: vscode, jetbrains, claude
```

| `--ide` | Writes |
|---|---|
| `claude` | `.mcp.json` and `.claude/skills/gg/SKILL.md`, a skill that teaches Claude Code (and Cursor) the workflow and every tool. |
| `vscode` | `.vscode/mcp.json` and three Copilot agent-mode agents in `.github/agents/`: `gg-journey` (write and verify a file), `gg-pilot` (run a mission) and `gg-triage` (read-only snapshot analysis). |
| `jetbrains` | `.mcp.json`, which JetBrains' AI Assistant and ACP agents read. |
| `all` (default) | All of the above. |

It never overwrites a file that differs without `--force`, merges into an existing `.mcp.json` instead of replacing it, and running it twice changes nothing. `--print` shows what it would write. To also list gg as an agent in JetBrains, see [the ACP agent](/autopilot#in-your-ide-the-acp-agent).

Prefer to wire it up yourself? The sections below show each client.

### Claude Code

```bash
claude mcp add gg -- gg mcp
```

With a tighter leash:

```bash
claude mcp add gg -- gg mcp --allow-host localhost:8080 --max-rps 100 --max-duration 5m
```

### Cursor

Add to `.cursor/mcp.json` (project) or `~/.cursor/mcp.json` (all projects):

```json
{
  "mcpServers": {
    "gg": { "command": "gg", "args": ["mcp", "--allow-host", "localhost:8080"] }
  }
}
```

### VS Code

Add to `.vscode/mcp.json`:

```json
{
  "servers": {
    "gg": { "type": "stdio", "command": "gg", "args": ["mcp", "--allow-host", "localhost:8080"] }
  }
}
```

### JetBrains IDEs

In **Settings → Tools → AI Assistant → Model Context Protocol (MCP)**, add a server and paste:

```json
{
  "mcpServers": {
    "gg": { "command": "gg", "args": ["mcp", "--allow-host", "localhost:8080"] }
  }
}
```

### Any other client

Run `gg mcp` as a stdio command, or use [HTTP](#over-http-for-remote-and-containerized-agents). Nothing but JSON-RPC is ever written to stdout; logs go to stderr (`--log-level` controls them).

---

## What the agent can do

A typical request: *"Validate `api.http`, dry-run it, run it for 20 seconds at 20 rps, nudge it up by 10 and mark when you did, stop, and compare it with the `baseline` snapshot."* The agent calls the tools below in that order, entirely on its own.

| Tool | What it does | Sends traffic? |
|---|---|---|
| [`validate_http_file`](/mcp-tools#validate_http_file) | Check a `.http` file; list journeys and problems with line numbers. | No |
| [`run_journey_dry_run`](/mcp-tools#run_journey_dry_run) | Run each journey once against the real target; show status, timing and extracted variables. | Yes, once per request |
| [`generate_config`](/mcp-tools#generate_config) | Build a valid gg YAML config from a profile or explicit stages. | No |
| [`start_simulation`](/mcp-tools#start_simulation) | Start a load test in the background, optionally under the built-in [autopilot](/autopilot); returns a `session_id`. | Yes |
| [`get_live_telemetry`](/mcp-tools#get_live_telemetry) | Current state, RPS, latency percentiles, error rate, recent windowed health and the worst endpoints. Can wait for a change instead of polling. | No |
| [`steer_traffic`](/mcp-tools#steer_traffic) | Change the rate (`adjust_rps`, `set_rps`), pin it (`hold_rps`, `release_hold`), label a moment (`mark`) or hand the rate to the guard (`autopilot_start`, `autopilot_stop`). | Changes load |
| [`stop_simulation`](/mcp-tools#stop_simulation) | Stop cleanly; returns final statistics, the snapshot path and, on request, a shareable report. | No |
| [`snap_list`](/mcp-tools#snap_list) | List saved snapshots, newest first, with headline numbers. | No |
| [`snap_capture`](/mcp-tools#snap_capture) | Summarise a snapshot, per endpoint. | No |
| [`snap_diff`](/mcp-tools#snap_diff) | Compare two snapshots endpoint by endpoint. | No |
| [`snap_assert`](/mcp-tools#snap_assert) | Pass/fail check against a baseline, with the thresholds of `gg snap assert`. | No |

The server also offers three **resources** the agent can read for reference (`gg://docs/http-syntax`, `gg://schema/gg-yaml` and `gg://profiles/catalog` (the built-in [load profiles](/profiles))) and three **prompts**: `author_journey` (write and verify a journey), `find_ceiling` (ramp load to find the capacity limit) and `regression_check` (compare a run against a baseline).

### Watching without polling

An agent that polls every second burns its context window on near-identical readings. `get_live_telemetry` takes `wait_s` (up to 30) and returns as soon as something worth reading happens: the run ends, or a `wake_on_p99_ms`, `wake_on_error_rate` or `wake_on_any_error` threshold is met on the **windowed** figures (the last few seconds, so a recovery shows up before it shows in the run totals). The reply says how long it waited and why it woke, with the window's range. A typical loop is one call every few seconds that only returns early when there is news.

Arguments, example calls and results for each tool are in the [MCP Tool Reference](/mcp-tools). Tool results are compact JSON, sized so that a long run does not flood the agent's context. A typical live-telemetry reading is a few hundred bytes; lists such as endpoints or violations are capped and say how many were left out.

---

## Safety

The server is built so an agent cannot do more than you allowed, whatever it is told to do.

| Flag | Default | What it limits |
|---|---|---|
| `--max-rps` | 1000 | The highest RPS any simulation may use, including after steering. |
| `--max-duration` | 10m | The longest any simulation may run. |
| `--max-sessions` | 2 | Simulations an agent may run at the same time. |
| `--max-user-sessions` | 1 | Simulations the IDE plugin may run at the same time (HTTP mode only). |
| `--allow-host` | any host | Hosts (or `host:port`) a `.http` file may target. Repeatable or comma-separated. |

When a request would break a cap, the tool refuses and says which flag raises the limit.

- **Restrict targets.** Set `--allow-host` to the services you own. With it set, a request whose *host* comes from a `{{variable}}` is refused, because the server cannot verify it. Variables in the path or query are fine. The dry-run tool checks every redirect hop. During a load run, a redirect from an allowed host to another host is **not** checked, so only allow hosts you trust not to redirect elsewhere.
- **Agents cannot steer your runs.** Every simulation has an owner: a run started by an agent is `agent`, one started from the IDE plugin is `user`. The caps above apply to agent runs and agent steering only, so plugin runs stay as capable as plain `gg`. An agent can read a user run but cannot steer or stop it unless you switch on **agent steering** in the plugin; until then it gets `user_session_locked`. When both kinds exist, an agent call with no `session_id` picks the agent's own run.
- **Dry runs are real requests.** They include `POST`, `PUT` and `DELETE`. Use a test environment.
- **Secrets stay out of the output.** Tokens, passwords, cookies and authorization values are masked in dry-run results, and response previews are truncated.
- **Simulations belong to the server, not to a connection.** Any client that passes the token check can use any `session_id`, and the session cap is shared. Over stdio that is just your one client; over HTTP with no token on loopback it is any process on the machine.
- **Clean shutdown.** When the client disconnects or the server gets SIGTERM, every running simulation is stopped first and its snapshot is written.
- **Snapshots** are written where `gg snap` writes them: `$GG_SNAP_DIR`, or `gg/snapshots` under your user config directory.

---

## Over HTTP, for remote and containerized agents

```bash
gg mcp --port 8765 --auth-token "$GG_MCP_TOKEN"
```

This serves [Streamable HTTP](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports#streamable-http) at `http://127.0.0.1:8765/mcp`. Use `--port 0` to pick a free port; the URL is printed on stderr.

| Flag | Meaning |
|---|---|
| `--port` | Selects HTTP. `0` picks a free port. |
| `--bind` | Address to listen on, default `127.0.0.1`. |
| `--auth-token` | Bearer token clients must send (or set `GG_MCP_TOKEN`; the flag wins). At least 16 characters. |
| `--allow-origin` | Browser origin allowed to call the endpoint (repeatable). Default none. |

Rules the server enforces:

- **A non-loopback `--bind` requires a token.** `--bind 0.0.0.0` without one exits with an error before listening.
- **The token travels as `Authorization: Bearer <token>`** and applies on loopback too whenever one is set. It is never written to the logs.
- **The `Host` header must be loopback on a loopback listener**, which blocks DNS-rebinding attacks.
- **Any request carrying an `Origin` header is refused** unless that exact origin is in `--allow-origin`. MCP clients do not send one; browsers do.

Connecting Claude Code to it:

```bash
claude mcp add --transport http gg http://127.0.0.1:8765/mcp --header "Authorization: Bearer $GG_MCP_TOKEN"
```

---

## Sharing runs with your IDE

Run `gg mcp` as a long-lived daemon and everything an agent starts shows up live in the IDE plugin's dashboard, with the same Stop and bias controls, and a run started from the plugin is visible to the agent:

```bash
gg mcp --port 0 --exit-on-stdin-eof
```

On start it prints one JSON line to stdout with the bound URL, and records itself in a per-workspace discovery file (mode `0600`, in your user cache directory, because it holds the token). `--exit-on-stdin-eof` makes the daemon stop every run and exit when its parent closes stdin, so it never outlives the IDE.

| Endpoint | Purpose |
|---|---|
| `/mcp` | The MCP endpoint, as above. |
| `GET /plugin/events` | Server-sent events: every run's live Live Control Protocol stream, wrapped with `session_id`, `origin` and autopilot state. A new subscriber is caught up first, and a slow one gets a `lagged` notice instead of stalling the run. |
| `POST /plugin/start` | Start a run as `user`. |
| `POST /plugin/control` | Send a Live Control Protocol command (`bias`, `hold`, `mark`, `stop`, `autopilot`) to a run, recorded as `source: "user"`. |

They sit behind the same token and loopback checks as `/mcp`. The shapes are documented for plugin authors in the [Live Control Protocol](/control-protocol#the-ide-bridge).

Clients that only speak stdio can join the same daemon:

```bash
gg mcp --attach                 # finds the daemon for the current directory
gg mcp --attach http://127.0.0.1:8765/mcp
```

`--attach` bridges stdio to the daemon in both directions, including cancellation and blocking `wait_s` calls. With no daemon running it falls back to serving in-process, and a stale discovery file is ignored.

The JetBrains plugin uses this bridge (`gg` v1.5.0+). **Tools → Connect AI Agents to Gopher Glide…** links the workspace's `.mcp.json` to the IDE's daemon, and a run an agent starts opens in the Run tab with a **STARTED BY AI AGENT** badge. See [IDE Plugins](/plugin).

![A JetBrains Air agent session beside the plugin's Run tab, which shows the run the agent started with a Started by AI Agent badge](/assets/plugin-agent-run.png)

---

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| The client shows no gg tools | `gg` is not on the client's `PATH`; use an absolute path as the command. Run `gg mcp` by hand: it should print a `gg mcp ready` line on stderr and wait. |
| `peak N RPS is above the server limit` | The request exceeds `--max-rps` or `--max-duration`; raise the flag, or ask for less. |
| `host ... is not allowed` | The target is not in `--allow-host`. |
| `start_simulation` says the cap on simulations is reached | Stop a finished or unwanted run with `stop_simulation`, or raise `--max-sessions`. |
| `user_session_locked` | The run was started from the IDE plugin. Turn on agent steering there, or let the agent start its own run. |
| "the autopilot owns the rate right now" | The built-in autopilot is running. Call `steer_traffic` with `autopilot_stop` first, then steer. |
| A garbled stream in a custom client | Read stdout only for JSON-RPC frames; everything else is on stderr. |

See also: [Autopilot](/autopilot), [CLI Reference](/cli-reference#gg-mcp-serve-gg-to-ai-agents), [Snapshots](/snap), [Driving a Run Live](/live-control).
