---
title: "CLI Reference"
description: "Every gg command and flag, in one place."
---

A complete, man-page-style reference for every `gg` command and flag. For the conceptual walkthroughs, see [Getting Started](/getting-started), [Load Profiles](/profiles), [Autopilot](/autopilot), [Snapshots](/snap), and [Configuration](/configuration).

You can always check this against the binary you have installed:

```bash
gg --help
gg snap --help        # per-subcommand: gg snap <list|view|diff|assert|prune|report> --help
gg profile --help
gg mcp --help
gg autopilot --help
```

---

## `gg`: run a load test

```bash
gg [config-file] [flags]
```

`config-file` is an optional positional argument (a `config.yaml` path). When omitted, `gg` falls back to an in-memory default config: you'll need `--http-file` (or `httpFile` in a config) for it to know what to hit.

### Config & profile

| Flag | Default | Description |
|---|---|---|
| `--config <path>`, `-c` | - | Path to a config YAML file. Overrides the positional argument. |
| `--http-file <path>` | - | Path to a `.http` requests file. Overrides `httpFile` in the config. |
| `--profile <name>`, `-p` | - | Name of a built-in or custom load profile to run (e.g. `flash-sale`, `load`, `stress`). See [Load Profiles](/profiles). |
| `--peak-rps <n>`, `-r` | profile default | Peak RPS for the profile. Overrides the profile's `default_peak_rps`. |
| `--duration <dur>`, `-d` | profile default | Total run duration for the profile (e.g. `1h`, `90s`). Overrides the profile's `default_duration`. |

### Snapshot capture

| Flag | Default | Description |
|---|---|---|
| `--snap` | `false` | Capture a behavioral snapshot after the run. Implied automatically if any other `--snap-*` flag is set. |
| `--snap-tag <tag>` | - | Tag to attach to the snapshot (e.g. `v1.2.0-pre`). |
| `--snap-dir <path>` | OS config dir | Override the default snapshot directory. See [Where are Snapshots saved?](/snap#where-are-snapshots-saved) |
| `--snap-sample <0-1>` | `0.05` | Fraction of responses to body-sample for schema inference. |
| `--snap-max-samples <n>` | `200` | Max body samples retained per endpoint via reservoir sampling. |
| `--snap-max-body-kb <n>` | `0` (no limit) | Per-endpoint byte budget for stored body samples, in KB. |

### Built-in autopilot

Let `gg` steer the rate for you. See [Autopilot](/autopilot) for how the two modes behave.

| Flag | Default | Description |
|---|---|---|
| `--autopilot <mode>` | off | `find-ceiling` (discover the highest sustainable RPS; sets its own schedule, so it cannot be combined with `--profile`) or `slo-guard` (clamp the rate on an SLO breach while a profile or config runs). |
| `--max-p99 <dur>` | `250ms` | p99 latency limit, e.g. `200ms`. |
| `--max-error-rate <0-1>` | `0.01` | Error-rate limit as a fraction. |
| `--min-samples <n>` | `50` | Latency samples a window needs before it is judged. |
| `--step-interval <dur>` | `5s` | Time between rate steps. |
| `--max-rps <n>` | `1000` | `find-ceiling`: never exceed this rate. |
| `--start-rps <n>` | `20` | `find-ceiling`: first rate to hold. |
| `--step-pct <n>` | `25` | `find-ceiling`: percent to raise the rate each step. |
| `--confirm-hold <dur>` | `15s` | `find-ceiling`: how long to confirm the backed-off rate. |
| `--recover-hold <dur>` | `10s` | `slo-guard`: how long health must stay in margin before releasing. |

With `find-ceiling`, `--duration` is the longest the search may run (default 5 minutes). In the TUI the arrow keys are ignored while the controller owns the rate.

### Mission report

| Flag | Default | Description |
|---|---|---|
| `--report <file>` | - | When the run ends, write a shareable [mission report](/snap#mission-reports) to this file: HTML for a `.html` name, Markdown otherwise. Implies `--snap`. If the report fails, the snapshot is still kept. |

### Headless mode & TUI output

| Flag | Default | Description |
|---|---|---|
| `--headless` | `false` | Run without the interactive TUI: emits structured heartbeat logs to stdout instead. Use this in CI. |
| `--reporter <text\|json>` | `text` | Output format in headless mode. `json` emits one [`HeartbeatPayload`](#headless-heartbeat-payload) object per line. |
| `--control <stdin\|none>` | `stdin` | In headless mode, `stdin` listens for `bias`/`mark`/`stop` commands on this process's own standard input; `none` disables the listener for locked-down CI. Not used in TUI mode. See [Driving a Run Live](/live-control) and the [Live Control Protocol](/control-protocol). |
| `--control-exit-on-eof` | off | Headless only, with `--control stdin`: when standard input closes, stop gracefully (as if a `stop` command arrived) and finish the snapshot. Meant for a supervisor that launches `gg` and may exit before it does; without it, a closed stdin is not a stop. |
| `--heartbeat-interval <dur>` | `5s` | Headless heartbeat cadence, e.g. `2s` for smoother dashboard repaints. `0` or a negative value falls back to the default. |
| `--tui-tick-ms <n>` | `42` (~24fps) | Interactive TUI redraw cadence in milliseconds. Clamped to `[16, 100]`: below 16ms wastes CPU on redraws no terminal can render that fast anyway; above 100ms the UI starts to feel unresponsive. `0` uses the default. Ignored in `--headless` mode. |

#### Headless heartbeat payload

Each line in `--reporter json` mode is one JSON object. `event` is one of `started`, `heartbeat`, `snap`, `interrupted`, `finished`, `ack`, `error`, `mark`, `stopped`, `autopilot`. The last five are replies/events on the [Live Control Protocol](/control-protocol)'s stdin channel. `stopped` replaces `finished` when a run ends via a `stop` command, and carries the same final statistics.

```json
{"time":"2026-06-20T01:00:24Z","event":"started","total_stages":2,
 "stages":[{"name":"Spike","duration_seconds":0,"target_rps":10},
           {"name":"Sustain","duration_seconds":5,"target_rps":10}],
 "profile":"smoke","profile_scale":1,"message":"Load test started (profile=smoke (scale=1.00)) 2 stage(s)"}
```

- `stages`: the full stage plan (name inferred the same way the TUI infers it; see [Stage inference rules](/configuration#stage-inference-rules)), sent once on `started`.
- `profile` / `profile_scale`: only present when the run was launched via `--profile`; omitted entirely for plain config/`.http`-file runs.
- `heartbeat` events additionally carry `stage`, `total_stages`, `target_rps`, `actual_rps`, `total_requests`, `success_count`, `failure_count`, `error_rate`, `p50_ms`, `p95_ms`, `p99_ms`, `bias` (cumulative Director bias, `0` if none applied). `bias` is present on heartbeats and on the `ack` for a `bias` command only: not on other events.
- `heartbeat` events may also carry `error_mix` (v1.5.0+): the kinds of failures in the last 10 seconds (`transport`, `http_4xx`, `http_5xx`, `extract`, `top_codes`, `top_endpoints`, `capped`). Present only when something failed in that window; see [Heartbeat additions](/control-protocol#-heartbeat-additions).
- From v1.5.0, heartbeats also carry `elapsed_s`, `window` (p99 and error rate over the last few seconds), `hold_rps` (only while a hold is active) and `autopilot` (only while one runs). See [v1.5: windowed health, time and the pinned rate](/control-protocol#v15-windowed-health-time-and-the-pinned-rate).
- `started` also carries `protocol_version` and `capabilities`: see [Versioning & capabilities](/control-protocol#versioning--capabilities).

---

## `gg version` / `--version` / `-v`

Prints the version, commit, and build date, then exits.

```bash
gg --version
gg version    # equivalent
```

---

## `gg snap`: manage snapshots

```bash
gg snap <subcommand> [flags]
```

See [Snapshots (gg snap)](/snap) for the full walkthrough.

### `gg snap list`

```bash
gg snap list [--snap-dir DIR] [--reporter text|json]
```

### `gg snap view <id|tag|file>`

```bash
gg snap view <id|tag|file> [--snap-dir DIR] [--reporter text|json]
```

### `gg snap diff <id1|tag1|file1> <id2|tag2|file2>`

```bash
gg snap diff <baseline> <current> [--snap-dir DIR] [--max-array-bloat PCT] [--reporter text|json]
```

`--reporter json` on `list`, `view` and `diff` (v1.5+) prints the same structures the IDE plugins and agents read, so a script gets a stable shape: `list` → `{snap_dir, snapshots[]}` (oldest first, ids as in the text listing), `view` → `{ref, snapshot}`, `diff` → `{baseline_ref, current_ref, result}`. Errors still go to stderr with exit code 1.

### `gg snap report <id|tag|file>`

```bash
gg snap report <id|tag|file> [--snap-dir DIR] [--format md|html] [--out FILE]
```

Renders one snapshot as a shareable [mission report](/snap#mission-reports). Prints to stdout unless `--out` is given. `--format` defaults to the `--out` extension (`.html` or `.md`), else Markdown.

### `gg snap assert`

```bash
gg snap assert --baseline <id|tag|file> --current <id|tag|file> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--baseline <id\|tag\|file>` | - | **Required.** Baseline snapshot. |
| `--current <id\|tag\|file>` | - | **Required.** Current snapshot to compare against the baseline. |
| `--latency-regression <pct>` | `20` | P99 latency % increase that triggers `REGRESSION`. |
| `--error-rate-delta <0-1>` | `0.05` | Error rate absolute increase that triggers `REGRESSION`. |
| `--payload-size-delta <pct>` | `50` | Avg payload size % increase that triggers `WARN`. |
| `--deny-removed-fields` | `false` | Treat removed schema fields as `REGRESSION` instead of `WARN`. |
| `--max-array-bloat <pct>` | `0` | Array field avg-length % increase that triggers `REGRESSION`. `0` disables the check. |
| `--fail-on-warn` | `false` | Exit non-zero on `WARN` verdicts in addition to `REGRESSION`. |
| `--reporter <text\|json\|md>` | `text` | Output format. `md` is suited for posting as a CI/PR comment. |
| `--out <path>` | stdout | Write the report to this file instead of stdout. |
| `--snap-dir <path>` | OS config dir | Override the default snapshot directory. |

Exits non-zero when the comparison fails (`REGRESSION`, or `WARN` with `--fail-on-warn`).

### `gg snap prune`

```bash
gg snap prune [filter flags] [--dry-run] [--yes] [--reporter text|json]
```

At least one filter flag is required.

| Flag | Default | Description |
|---|---|---|
| `--keep-last <n>` | - | Keep the N most-recent snapshots; delete all older ones. |
| `--older-than <dur>` | - | Delete snapshots older than this duration (e.g. `30d`, `720h`). |
| `--tag <tag>` | - | Delete all snapshots whose tag matches this value exactly. |
| `--ids <1,3,5>` | - | Comma-separated list of snapshot IDs to delete. |
| `--dry-run` | `false` | Preview candidates without deleting anything. |
| `--yes` | `false` | Skip the interactive confirmation prompt (required for CI / non-interactive use). |
| `--reporter <text\|json>` | `text` | `json` is the stable contract used by the IDE plugins' Snap Explorer. |
| `--snap-dir <path>` | OS config dir | Override the default snapshot directory. |

---

## `gg profile`: explore & manage load profiles

```bash
gg profile <subcommand>
```

See [Load Profiles](/profiles) for the full catalog and scaling flags (`--peak-rps`, `--duration`, used on the root `gg` command, not here).

| Subcommand | Description |
|---|---|
| `gg profile list` | List all built-in and custom profiles. |
| `gg profile view <name>` | Show a profile's shape and ASCII chart. |
| `gg profile export <name>` | Copy a built-in profile to `~/.config/gg/profiles/` as a starting point for customization. Must be renamed before use: built-in names are reserved. |

---

## `gg mcp`: serve gg to AI agents

```bash
gg mcp [flags]
```

Runs gg as a [Model Context Protocol](/mcp) server so an AI agent can validate, run, steer and judge load tests. It speaks MCP over **stdio** by default (stdout carries only protocol messages; logs go to stderr). With `--port` it serves Streamable HTTP at `/mcp` instead.

### Safety caps

| Flag | Default | Description |
|---|---|---|
| `--max-rps <n>` | `1000` | Highest RPS any simulation may use, including after steering. |
| `--max-duration <d>` | `10m` | Longest any simulation may run. |
| `--max-sessions <n>` | `2` | Simulations an agent may run at the same time. |
| `--max-user-sessions <n>` | `1` | Simulations the IDE plugin may run at the same time (with `--port`). Not subject to the other caps. |
| `--allow-host <host[:port]>` | any | Restrict targets to this host. Repeatable or comma-separated. |

### Transport & logging

| Flag | Default | Description |
|---|---|---|
| `--port <n>` | - | Serve Streamable HTTP on this port instead of stdio. `0` picks a free port. |
| `--bind <addr>` | `127.0.0.1` | Address to listen on with `--port`. Anything but loopback requires an auth token. |
| `--auth-token <token>` | - | Bearer token clients must send with `--port`. Also read from `GG_MCP_TOKEN`; the flag wins. At least 16 characters. |
| `--allow-origin <origin>` | none | Browser origin allowed to call the HTTP endpoint, e.g. `https://example.com`. Repeatable. |
| `--log-level <level>` | `info` | Log verbosity on stderr: `debug`, `info`, `warn`, `error`. |

| Flag | Default | Description |
|---|---|---|
| `--exit-on-stdin-eof` | off | With `--port`: stop every simulation and exit when stdin closes. For a daemon an IDE supervises. |
| `--attach [url]` | - | Instead of serving, connect this stdio client to a running `gg mcp` daemon (found by working directory when no URL is given) and share its runs; serves in-process when there is none. Cannot be combined with `--port`, `--bind`, `--allow-origin` or `--exit-on-stdin-eof`. |

With `--port`, `gg mcp` also serves the IDE bridge at `/plugin/*`; see [Sharing runs with your IDE](/mcp#sharing-runs-with-your-ide). On start it prints a `{"event":"listening","url":…,"pid":…}` line on stdout.

`--bind`, `--auth-token` and `--allow-origin` are errors without `--port`. A bad flag exits with code `2` and writes nothing to stdout.

Snapshots written by agent runs go where `gg snap` reads them: `$GG_SNAP_DIR`, or `gg/snapshots` in your user config directory.

### `gg mcp init`

```bash
gg mcp init [--ide vscode|jetbrains|claude|all] [--dir DIR] [--force] [--print] [--gg-path PATH] [--register-acp [--acp-file FILE]]
```

Writes the files that connect an IDE agent to gg in the current directory. See [AI Agents (MCP) → One command](/mcp#one-command).

| Flag | Default | Description |
|---|---|---|
| `--ide <target>` | `all` | `vscode` (`.vscode/mcp.json` and `.github/agents/gg-*.agent.md`), `jetbrains` (`.mcp.json`), `claude` (`.mcp.json` and `.claude/skills/gg/SKILL.md`) or `all`. |
| `--dir <path>` | `.` | Workspace root to write into. |
| `--force` | off | Replace files and entries that differ. Without it, nothing is overwritten. |
| `--print` | off | Show what would be written and write nothing. |
| `--gg-path <path>` | `gg` if on `PATH`, else this binary | The command the config files run. |
| `--register-acp` | off | Also add **gg autopilot** to the user-wide `~/.jetbrains/acp.json`. Merges one entry, keeps everything else byte for byte, refuses invalid JSON. Restart the IDE afterwards. |
| `--acp-file <path>` | `~/.jetbrains/acp.json` | The file `--register-acp` edits. |

---

## `gg autopilot`: AI agents

```bash
gg autopilot generate [flags] <what the API does | path to an OpenAPI spec>
gg autopilot run      [flags] <mission goal>
gg autopilot triage   [flags] [question]
gg autopilot --acp    [flags]
```

LLM agents that drive gg through its MCP tools; not to be confused with the [`--autopilot`](#built-in-autopilot) flag of the root command, which is the built-in controller and needs no model. See [Autopilot](/autopilot#ai-agents-gg-autopilot).

| Flag | Default | Description |
|---|---|---|
| `--acp` | off | Serve the Agent Client Protocol on stdio (JetBrains IDEs, Zed). Slash commands work without a model. |
| `--model <name>` | `gemini-flash-latest` for Gemini | Model name. Required with `--base-url`. Also `GG_AUTOPILOT_MODEL`. |
| `--base-url <url>` | - | OpenAI-compatible chat endpoint, e.g. `http://localhost:11434/v1` for Ollama. Also `GG_AUTOPILOT_BASE_URL`. |
| `--out <file>` | `requests.http` | `generate`: where to save the verified `.http` file. |
| `--max-turns <n>` | `4` | `generate`: attempts before giving up. |
| `--mcp-url <url>` | `$GG_MCP_URL` | Use a running `gg mcp` daemon instead of an in-process server. |
| `--auth-token <token>` | `$GG_MCP_TOKEN` | Bearer token for `--mcp-url`. |
| `--max-rps`, `--max-duration`, `--allow-host` | as `gg mcp` | Caps for the in-process server. |
| `--log-level <level>` | `warn` | Log verbosity on stderr. |

The model comes from `GEMINI_API_KEY` (or `GOOGLE_API_KEY`, or Vertex AI via `GOOGLE_GENAI_USE_VERTEXAI=true`), or from `--base-url`. `GG_AUTOPILOT_API_KEY` supplies a key for an OpenAI-compatible server. Progress goes to stderr and the final answer to stdout.
