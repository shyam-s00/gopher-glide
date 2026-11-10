---
title: "MCP Tool Reference"
description: "Arguments, example calls and results for every tool the gg MCP server offers."
---

Reference for the ten tools served by [`gg mcp`](/mcp). For installing the server, the safety caps and HTTP mode, see [AI Agents (MCP)](/mcp). Every call below sends real traffic where marked, so read [Safety](/mcp#safety) first.

Each example shows the arguments the agent sends and the abbreviated result.

| Tool | Sends traffic? |
|---|---|
| [`validate_http_file`](#validate_http_file) | No |
| [`run_journey_dry_run`](#run_journey_dry_run) | Yes, once per request |
| [`generate_config`](#generate_config) | No |
| [`start_simulation`](#start_simulation) | Yes |
| [`get_live_telemetry`](#get_live_telemetry) | No |
| [`steer_traffic`](#steer_traffic) | Changes load |
| [`stop_simulation`](#stop_simulation) | No |
| [`snap_capture`](#snap_capture) | No |
| [`snap_diff`](#snap_diff) | No |
| [`snap_assert`](#snap_assert) | No |

## `validate_http_file`

Checks a `.http` file without sending anything. Give the text as `content` or a path on the machine running gg as `path`.

```json
{ "path": "journey.http" }
```

```json
{
  "ok": true, "requests": 3, "errors": [], "warnings": [],
  "journeys": [
    { "index": 1, "steps": [
      { "line": 2, "name": "login", "method": "POST", "url": "http://localhost:8080/login", "exports": ["token", "id"] },
      { "line": 8, "name": "get item", "method": "GET", "url": "http://localhost:8080/items/{{id}}", "uses": ["id", "token"] } ] },
    { "index": 2, "steps": [ { "line": 12, "name": "search", "method": "GET", "url": "http://localhost:8080/search?q=widget" } ] }
  ]
}
```

Errors and warnings carry line numbers (bad URLs, undefined `{{variables}}`, broken `@gg-export` extractors). Long lists are capped, with `omitted_issues` / `omitted_requests` telling the agent how much was left out.

## `run_journey_dry_run`

Runs each journey **once** against the real target so the agent can fix the file before generating load. A failing step ends its journey and later steps are skipped. The file is validated first and nothing is sent if it has errors.

| Argument | Meaning |
|---|---|
| `content` / `path` | The `.http` file (one of the two). |
| `variables` | Values for `{{name}}` placeholders no earlier request exports, such as an existing token. |
| `journeys` | 1-based journey numbers to run; default all. |
| `step_timeout_s` | Per-request timeout, default 10, max 60. |
| `body_preview_bytes` | Response bytes shown per step, default 512, max 2048. |

```json
{ "path": "journey.http" }
```

```json
{
  "result": { "ok": true, "steps_passed": 3, "steps_failed": 0, "steps_skipped": 0,
    "journeys": [ { "index": 1, "ok": true, "steps": [
      { "name": "login", "status": "passed", "http_status": 200, "duration_ms": 0.8,
        "extracted": { "id": "7", "token": "[redacted] (12 chars)" },
        "body_preview": "{\"token\":\"[redacted]\",\"id\":\"7\"}" },
      { "name": "get item", "status": "passed", "http_status": 200, "duration_ms": 7.2,
        "url": "http://localhost:8080/items/7" } ] } ] }
}
```

Secrets are masked in the output: tokens, passwords, cookies and authorization values never reach the agent.

## `generate_config`

Builds a valid gg YAML config from a built-in `profile` (optionally scaled with `peak_rps` and `duration`) or explicit `stages`, plus optional `jitter`, `time_scale` and `snap` settings. It returns the YAML or the validation errors, never an invalid config, and does not write files.

```json
{ "http_file": "api.http", "profile": "load", "peak_rps": 50, "duration": "5m" }
```

```json
{ "ok": true, "yaml": "config:\n  httpFile: api.http\nstages:\n  - ...", "errors": [], "warnings": [] }
```

If the request exceeds the server's own caps, `warnings` says so.

## `start_simulation`

Starts a real load test and returns immediately; the run continues in the background. The file is validated first, and the server refuses (naming the flag that raises the limit) when a cap would be exceeded.

| Argument | Meaning |
|---|---|
| `http_content` / `http_path` | The `.http` file (one of the two). |
| `profile`, `peak_rps`, `duration` | A built-in profile, optionally scaled. |
| `config_yaml` | A full gg config instead of a profile; its `httpFile` is replaced by the file you supplied. |
| `snap`, `snap_tag` | Record a [snapshot](/snap) when the run ends, with an optional tag (letters, digits, `.` `_` `-`). |

```json
{ "http_path": "journey.http", "profile": "canary", "peak_rps": 20, "duration": "20s", "snap": true, "snap_tag": "agent-demo" }
```

```json
{ "session_id": "sim_b6f013af50e6", "state": "starting", "peak_rps": 20, "duration_s": 20, "snapshot": true,
  "stages": [ { "name": "Spike", "duration_s": 0, "target_rps": 20 }, { "name": "Sustain", "duration_s": 20, "target_rps": 20 } ],
  "next": "Poll get_live_telemetry ... and call stop_simulation when done." }
```

Only built-in profiles can be named; custom shapes go through `config_yaml`.

## `get_live_telemetry`

Reads the current state of a run without waiting. `session_id` may be omitted when only one simulation exists.

```json
{ "session_id": "sim_b6f013af50e6" }
```

```json
{ "session_id": "sim_b6f013af50e6", "state": "running", "elapsed_s": 3.11, "planned_s": 20, "stage": "2/2 Sustain",
  "metrics": { "target_rps": 20, "actual_rps": 30, "p50_ms": 6.99, "p95_ms": 13.17, "p99_ms": 14.41,
               "error_rate": 0, "total_requests": 120, "success_count": 120, "failure_count": 0, "bias": 0, "age_s": 0.11 } }
```

States are `starting` (no reading yet), `running`, `stopping`, `finished`, `stopped` and `failed`. Numbers are the latest once-per-second reading, and `age_s` says how old it is. When failures occur, an `error_mix` object breaks down the last 10 seconds (transport, 4xx, 5xx, extraction failures, top status codes); it is a rolling window, so its counts are not comparable to the cumulative totals. A finished run includes final statistics and `snapshot_path`; a failed one includes the reason and the tail of gg's stderr. `recent_marks` and `recent_steers` list what `steer_traffic` already did.

> `actual_rps` can exceed the target when a journey has several requests, because each iteration sends all of them.

## `steer_traffic`

Changes a running simulation or annotates it. Only works while the run is `running`.

| `action` | Arguments | Effect |
|---|---|---|
| `adjust_rps` | `delta` (signed, not zero) | Change the rate by `delta`. |
| `set_rps` | `target_rps` (1 up to `--max-rps`) | Aim for an absolute rate. |
| `mark` | `label`, optional `at_elapsed_s` | Label the current moment, or a time the agent saw in telemetry. |

```json
{ "session_id": "sim_b6f013af50e6", "action": "adjust_rps", "delta": 10 }
```

```json
{ "action": "adjust_rps", "applied": true, "delta": 10, "bias_total": 10, "target_rps_before": 20,
  "note": "applies within about 1 s; the profile may keep ramping, so check get_live_telemetry for the resulting rate" }
```

```json
{ "session_id": "sim_b6f013af50e6", "action": "mark", "label": "bumped to 30" }
```

```json
{ "action": "mark", "applied": true, "label": "bumped to 30", "elapsed_s": 6.36 }
```

Rate changes are offsets on top of the profile (the same [bias](/live-control) as in the TUI), take effect within about a second, and are limited by `--max-rps`. Marks and rate changes are saved in the snapshot, so a later diff can tell a regression from a human leaning on the throttle.

## `stop_simulation`

Stops a run cleanly and returns its final statistics. gg finishes writing the snapshot before exiting. Safe to call again, or on a run that already ended.

```json
{ "session_id": "sim_b6f013af50e6" }
```

```json
{ "state": "stopped", "elapsed_s": 8.76, "planned_s": 20,
  "message": "stopped early at 9s of 20s planned; the final statistics and any snapshot cover only that part",
  "final": { "total_requests": 330, "failure_count": 3, "error_rate": 0.0091, "p50_ms": 6.9, "p95_ms": 13.29, "p99_ms": 14.25 },
  "snapshot_path": "/home/you/.config/gg/snapshots/agent-demo-20261110-020147.snap" }
```

A run that does not stop in time is terminated; if it was cut short, the result says the statistics and snapshot cover only that part.

## `snap_capture`

Summarises a snapshot: when and how the run went (profile, peak RPS, total requests, marks, manual rate changes) and, per endpoint, request count, error rate, latency percentiles, top status codes and response-schema field counts, busiest first. Give a `session_id` of a finished run that used `snap: true`, or a `snapshot` (a `.snap` path, or a tag meaning the newest snapshot with it). `max_endpoints` defaults to 20, max 50.

```json
{ "snapshot": "agent-demo" }
```

## `snap_diff`

Compares two snapshots endpoint by endpoint: an overall verdict (`PASS`, `WARN`, `REGRESSION`), counts, and the endpoints worst first with latency change, error-rate change, payload change, status-code shifts and response-schema changes. New and removed endpoints are flagged. If either run was steered by hand, the result says so.

```json
{ "baseline": "baseline", "current": "agent-demo" }
```

```json
{ "overall": "WARN", "counts": { "pass": 2, "warn": 1, "regression": 0 },
  "endpoints": [ { "id": "GET:http://localhost:8080/search?q=widget", "verdict": "PASS",
    "compared": { "p99_pct": -1.13, "error_rate_delta": -0.008, "status_shift": [ { "code": "503", "share": -0.008 } ] } } ],
  "advisory": "Traffic was steered by hand during at least one of these runs ..." }
```

## `snap_assert`

A pass/fail check with the same defaults as [`gg snap assert`](/cli-reference#gg-snap-assert): `passed` is false when any endpoint regressed (P99 latency up more than 20%, error rate up more than 5 points, a response field changing type), and each violation names the endpoint and the breach.

| Argument | Default | Meaning |
|---|---|---|
| `latency_p99_regression_pct` | 20 | P99 increase that counts as a regression. |
| `error_rate_delta` | 0.05 | Error-rate increase (0–1) that counts as a regression. |
| `payload_size_pct` | 50 | Average response-size increase that counts as a warning. |
| `deny_removed_fields` | off | Treat a vanished response field as a regression. |
| `max_array_bloat_pct` | off | Array length growth that counts as a regression. |
| `fail_on_warn` | off | Also fail on warnings. |
| `max_violations` | 20 (max 100) | Violations to list. |

```json
{ "baseline": "baseline", "current": "agent-demo", "fail_on_warn": true }
```

Snapshot references are a `.snap` path or a tag. Numeric ids are not accepted.
