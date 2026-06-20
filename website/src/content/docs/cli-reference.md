---
title: "CLI Reference"
description: "Every gg command and flag, in one place."
---

A complete, man-page-style reference for every `gg` command and flag. For the conceptual walkthroughs, see [Getting Started](/getting-started), [Load Profiles](/profiles), [Snapshots](/snap), and [Configuration](/configuration).

You can always check this against the binary you have installed:

```bash
gg --help
gg snap --help        # per-subcommand: gg snap <list|view|diff|assert|prune> --help
gg profile --help
```

---

## `gg` — run a load test

```bash
gg [config-file] [flags]
```

`config-file` is an optional positional argument (a `config.yaml` path). When omitted, `gg` falls back to an in-memory default config — you'll need `--http-file` (or `httpFile` in a config) for it to know what to hit.

### Config & profile

| Flag | Default | Description |
|---|---|---|
| `--config <path>`, `-c` | — | Path to a config YAML file. Overrides the positional argument. |
| `--http-file <path>` | — | Path to a `.http` requests file. Overrides `httpFile` in the config. |
| `--profile <name>`, `-p` | — | Name of a built-in or custom load profile to run (e.g. `flash-sale`, `load`, `stress`). See [Load Profiles](/profiles). |
| `--peak-rps <n>`, `-r` | profile default | Peak RPS for the profile. Overrides the profile's `default_peak_rps`. |
| `--duration <dur>`, `-d` | profile default | Total run duration for the profile (e.g. `1h`, `90s`). Overrides the profile's `default_duration`. |

### Snapshot capture

| Flag | Default | Description |
|---|---|---|
| `--snap` | `false` | Capture a behavioral snapshot after the run. Implied automatically if any other `--snap-*` flag is set. |
| `--snap-tag <tag>` | — | Tag to attach to the snapshot (e.g. `v1.2.0-pre`). |
| `--snap-dir <path>` | OS config dir | Override the default snapshot directory. See [Where are Snapshots saved?](/snap#where-are-snapshots-saved) |
| `--snap-sample <0-1>` | `0.05` | Fraction of responses to body-sample for schema inference. |
| `--snap-max-samples <n>` | `200` | Max body samples retained per endpoint via reservoir sampling. |
| `--snap-max-body-kb <n>` | `0` (no limit) | Per-endpoint byte budget for stored body samples, in KB. |

### Headless mode & TUI output

| Flag | Default | Description |
|---|---|---|
| `--headless` | `false` | Run without the interactive TUI — emits structured heartbeat logs to stdout instead. Use this in CI. |
| `--reporter <text\|json>` | `text` | Output format in headless mode. `json` emits one [`HeartbeatPayload`](#headless-heartbeat-payload) object per line. |
| `--heartbeat-interval <dur>` | `5s` | Headless heartbeat cadence, e.g. `2s` for smoother dashboard repaints. `0` or a negative value falls back to the default. |
| `--tui-tick-ms <n>` | `42` (~24fps) | Interactive TUI redraw cadence in milliseconds. Clamped to `[16, 100]` — below 16ms wastes CPU on redraws no terminal can render that fast anyway; above 100ms the UI starts to feel unresponsive. `0` uses the default. Ignored in `--headless` mode. |

#### Headless heartbeat payload

Each line in `--reporter json` mode is one JSON object. `event` is one of `started`, `heartbeat`, `snap`, `interrupted`, `finished`.

```json
{"time":"2026-06-20T01:00:24Z","event":"started","total_stages":2,
 "stages":[{"name":"Spike","duration_seconds":0,"target_rps":10},
           {"name":"Sustain","duration_seconds":5,"target_rps":10}],
 "profile":"smoke","profile_scale":1,"message":"Load test started — profile=smoke (scale=1.00) — 2 stage(s)"}
```

- `stages` — the full stage plan (name inferred the same way the TUI infers it; see [Stage inference rules](/configuration#stage-inference-rules)), sent once on `started`.
- `profile` / `profile_scale` — only present when the run was launched via `--profile`; omitted entirely for plain config/`.http`-file runs.
- `heartbeat` events additionally carry `stage`, `total_stages`, `target_rps`, `actual_rps`, `total_requests`, `success_count`, `failure_count`, `error_rate`, `p50_ms`, `p95_ms`, `p99_ms`.

---

## `gg version` / `--version` / `-v`

Prints the version, commit, and build date, then exits.

```bash
gg --version
gg version    # equivalent
```

---

## `gg snap` — manage snapshots

```bash
gg snap <subcommand> [flags]
```

See [Snapshots (gg snap)](/snap) for the full walkthrough.

### `gg snap list`

```bash
gg snap list [--snap-dir DIR]
```

### `gg snap view <id|tag|file>`

```bash
gg snap view <id|tag|file> [--snap-dir DIR]
```

### `gg snap diff <id1|tag1|file1> <id2|tag2|file2>`

```bash
gg snap diff <baseline> <current> [--snap-dir DIR]
```

### `gg snap assert`

```bash
gg snap assert --baseline <id|tag|file> --current <id|tag|file> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--baseline <id\|tag\|file>` | — | **Required.** Baseline snapshot. |
| `--current <id\|tag\|file>` | — | **Required.** Current snapshot to compare against the baseline. |
| `--latency-regression <pct>` | `20` | P99 latency % increase that triggers `REGRESSION`. |
| `--error-rate-delta <0-1>` | `0.05` | Error rate absolute increase that triggers `REGRESSION`. |
| `--payload-size-delta <pct>` | `50` | Avg payload size % increase that triggers `WARN`. |
| `--deny-removed-fields` | `false` | Treat removed schema fields as `REGRESSION` instead of `WARN`. |
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
| `--keep-last <n>` | — | Keep the N most-recent snapshots; delete all older ones. |
| `--older-than <dur>` | — | Delete snapshots older than this duration (e.g. `30d`, `720h`). |
| `--tag <tag>` | — | Delete all snapshots whose tag matches this value exactly. |
| `--ids <1,3,5>` | — | Comma-separated list of snapshot IDs to delete. |
| `--dry-run` | `false` | Preview candidates without deleting anything. |
| `--yes` | `false` | Skip the interactive confirmation prompt (required for CI / non-interactive use). |
| `--reporter <text\|json>` | `text` | `json` is the stable contract used by the JetBrains plugin's Snap Explorer. |
| `--snap-dir <path>` | OS config dir | Override the default snapshot directory. |

---

## `gg profile` — explore & manage load profiles

```bash
gg profile <subcommand>
```

See [Load Profiles](/profiles) for the full catalog and scaling flags (`--peak-rps`, `--duration`, used on the root `gg` command, not here).

| Subcommand | Description |
|---|---|
| `gg profile list` | List all built-in and custom profiles. |
| `gg profile view <name>` | Show a profile's shape and ASCII chart. |
| `gg profile export <name>` | Copy a built-in profile to `~/.config/gg/profiles/` as a starting point for customization. Must be renamed before use — built-in names are reserved. |
