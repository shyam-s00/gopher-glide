---
title: "AI Agents (MCP)"
description: "Let an AI agent write, check, run and judge load tests with gg — over the Model Context Protocol, with hard safety caps."
---

`gg mcp` turns gg into a [Model Context Protocol](https://modelcontextprotocol.io) server. An agent in Claude Code, Cursor, VS Code or a JetBrains IDE can then do the whole loop on its own: **validate** a `.http` file, **dry-run** it against the real service, **start** a load test, **read live telemetry**, **steer** the traffic and **drop marks**, **stop**, and **diff the result against a baseline snapshot**.

Available from gg v1.4. Everything the agent does goes through the same engine, [Live Control Protocol](/control-protocol) and [snapshots](/snap) you already use, and every run is bounded by caps you set when you start the server.

> **This sends real traffic.** `start_simulation`, `steer_traffic` and `run_journey_dry_run` hit whatever the `.http` file points at, including `POST`/`PUT`/`DELETE`. Point agents at services you own, and read [Safety](#safety) before exposing the server to anything but your own machine.

---

## Install

You need `gg` v1.4 or newer on your `PATH` (see [Getting Started](/getting-started)). The server speaks MCP over **stdio** by default, so the client starts it for you.

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
| [`start_simulation`](/mcp-tools#start_simulation) | Start a load test in the background; returns a `session_id`. | Yes |
| [`get_live_telemetry`](/mcp-tools#get_live_telemetry) | Current state, RPS, latency percentiles, error rate and recent error kinds. | No |
| [`steer_traffic`](/mcp-tools#steer_traffic) | Change the rate (`adjust_rps`, `set_rps`) or label a moment (`mark`). | Changes load |
| [`stop_simulation`](/mcp-tools#stop_simulation) | Stop cleanly; returns final statistics and the snapshot path. | No |
| [`snap_capture`](/mcp-tools#snap_capture) | Summarise a snapshot, per endpoint. | No |
| [`snap_diff`](/mcp-tools#snap_diff) | Compare two snapshots endpoint by endpoint. | No |
| [`snap_assert`](/mcp-tools#snap_assert) | Pass/fail check against a baseline, with the thresholds of `gg snap assert`. | No |

The server also offers three **resources** the agent can read for reference — `gg://docs/http-syntax`, `gg://schema/gg-yaml` and `gg://profiles/catalog` (the built-in [load profiles](/profiles)) — and three **prompts**: `author_journey` (write and verify a journey), `find_ceiling` (ramp load to find the capacity limit) and `regression_check` (compare a run against a baseline).

Arguments, example calls and results for each tool are in the [MCP Tool Reference](/mcp-tools). Tool results are compact JSON, sized so that a long run does not flood the agent's context. A typical live-telemetry reading is a few hundred bytes; lists such as endpoints or violations are capped and say how many were left out.

---

## Safety

The server is built so an agent cannot do more than you allowed, whatever it is told to do.

| Flag | Default | What it limits |
|---|---|---|
| `--max-rps` | 1000 | The highest RPS any simulation may use, including after steering. |
| `--max-duration` | 10m | The longest any simulation may run. |
| `--max-sessions` | 2 | Simulations running at the same time. |
| `--allow-host` | any host | Hosts (or `host:port`) a `.http` file may target. Repeatable or comma-separated. |

When a request would break a cap, the tool refuses and says which flag raises the limit.

- **Restrict targets.** Set `--allow-host` to the services you own. With it set, a request whose *host* comes from a `{{variable}}` is refused, because the server cannot verify it. Variables in the path or query are fine. The dry-run tool checks every redirect hop. During a load run, a redirect from an allowed host to another host is **not** checked, so only allow hosts you trust not to redirect elsewhere.
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

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| The client shows no gg tools | `gg` is not on the client's `PATH`; use an absolute path as the command. Run `gg mcp` by hand: it should print a `gg mcp ready` line on stderr and wait. |
| `peak N RPS is above the server limit` | The request exceeds `--max-rps` or `--max-duration`; raise the flag, or ask for less. |
| `host ... is not allowed` | The target is not in `--allow-host`. |
| `start_simulation` says the cap on simulations is reached | Stop a finished or unwanted run with `stop_simulation`, or raise `--max-sessions`. |
| A garbled stream in a custom client | Read stdout only for JSON-RPC frames; everything else is on stderr. |

See also: [CLI Reference](/cli-reference#gg-mcp--serve-gg-to-ai-agents), [Snapshots](/snap), [Driving a Run Live](/live-control).
