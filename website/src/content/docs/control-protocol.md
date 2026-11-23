---
title: "Live Control Protocol"
description: "Drive a running gg process from stdin: bias, hold, mark, start an autopilot and stop a headless load test without killing it. The reference for plugin authors, scripts and the IDE bridge."
---

Headless mode used to be a one-way radio: `gg --headless` gave you a live heartbeat stream, but the only way to touch it was `Ctrl+C`. The **Live Control Protocol (LCP)** turns that into a two-way conversation: send commands in over stdin, get replies interleaved with the heartbeats out.

This is what the live bias/mark/stop controls in the [JetBrains plugin](/plugin#-native-run-dashboard) (7.0+) and the [VS Code extension](/plugin#live-control-in-vs-code) (3.0+) are built on. It's also directly usable,  no IDE required: type commands into a headless run, or script them. **New to this? Start with [Driving a Run Live](/live-control)**, a hands-on guide with a copy-paste example, the multi-terminal and container recipes, and troubleshooting. This page is the reference.

> **Commands today:** `bias`, `mark`, `stop`, plus `hold`, `release` and `autopilot` from gg v1.5. The protocol is versioned and capability-based, so newer commands never break older binaries and plugins (see [Versioning & capabilities](#versioning--capabilities) below).

---

## 🔌 Enabling it

The listener is on by default in headless mode:

```bash
gg config.yaml --headless --reporter json
```

Type (or pipe) a command as a single line of JSON and press enter. Commands are read from **the standard input of the `gg` process itself** (the terminal or pipe it was started in) so a command typed in a different terminal tab doesn't reach it (see [Where do commands go?](/live-control#-where-do-commands-go)). If stdin closes, the run is not affected: end-of-input is not a `stop`. To disable the listener entirely (locked-down CI, or a shell that forwards stdin somewhere else) pass `--control none`:

```bash
gg config.yaml --headless --control none
```

| Flag | Default | Description |
|---|---|---|
| `--control <stdin\|none>` | `stdin` | `stdin` listens for commands on stdin. `none` disables the listener; the run stays fully read-only, same as pre-v1.3 behavior. |

TUI mode is unaffected: the TUI already has `↑`/`↓` for bias (see [Director Mode](/tui#director-mode)); it doesn't gain a stdin listener.

---

## 📨 Commands

One JSON object per line (NDJSON):

```json
{"id":"c1","command":"bias","amount":5}
{"id":"c2","command":"bias","amount":-5}
{"id":"c3","command":"mark","label":"deploy-v2-canary"}
{"id":"c4","command":"stop"}
```

| Field | Description |
|---|---|
| `id` | Optional, caller-chosen. Echoed back in the reply so a script or plugin can correlate request ↔ response. Omit it if you're typing by hand. |
| `command` | `"bias"`, `"mark"`, `"stop"`, `"hold"`, `"release"` or `"autopilot"`. |
| `source` | Optional (v1.5+) on `bias`, `hold`, `release` and `mark`: who issued it, `"user"`, `"agent"` or `"autopilot"`. Echoed on the reply and saved in the snapshot, so a report can show who did what. Omitted means unattributed. Any other value is an `invalid_argument` error. |

### `bias`

Applies a signed delta on top of the current stage's target RPS: the same mechanism as the TUI's `↑`/`↓` keys, just with an arbitrary amount instead of a fixed ±5.

```json
{"id":"c1","command":"bias","amount":5}
```

`amount` must be a non-zero integer (positive or negative). The bias is cumulative (three `+5` commands add up to `+15`, exactly like pressing `↑` three times) and it rides on top of whatever the load profile is doing at that moment. No upper or lower bound is enforced by the protocol, but the effective rate never drops below 1 RPS.

The `ack` comes back immediately and already includes the new cumulative value. The request rate itself changes at the next one-second window, so the next heartbeat or two shows the effect.

### `mark`

Drops a labeled, timestamped annotation into the run: useful for narrating a game day ("canary deploy started here") so it shows up later when you diff the resulting snapshot.

```json
{"id":"c3","command":"mark","label":"deploy-v2-canary"}
```

`label` must be a non-empty string.

**`elapsed_s`** (optional, `gg` v1.5.0+): the moment, in seconds since the run started, that the mark belongs to:

```json
{"id":"c3","command":"mark","label":"deploy-v2-canary","elapsed_s":12.4}
```

Use it when the moment was captured earlier than the command is sent: a UI that pops up a label dialog after a click, say. Without it, the mark is stamped when `gg` receives the command. It must be a non-negative number (`null` counts as omitted); anything else is an `invalid_argument` error and no mark is recorded. A value later than the run's current time is capped to it, and the reply says `"clamped":true`. Measure the offset from when your client received the `started` event; `gg`'s own clock starts slightly earlier, so expect a difference of milliseconds.

Only a `gg` that lists the `control.mark.elapsed` [capability](#versioning--capabilities) honors the field. Older versions ignore it and stamp the mark with the receive time (no error) so check first and, if it's missing, send the mark without `elapsed_s`.

### `hold`

*(`gg` v1.5+, capability `control.hold`.)* Pins the target rate at a fixed value, ignoring the profile's ramp, until `release`. The run still ends on schedule. Use it to judge one rate on its own, or to build a ceiling search by hand.

```json
{"id":"c5","command":"hold","rps":120}
```

`rps` must be an integer of at least 1. The pinned rate replaces the stage target (a hold set during a ramp stays flat across a stage boundary), and the rate changes at the next one-second window. While a hold is active a `bias` is still recorded, but it does not move the pinned rate (the [MCP](/mcp-tools#steer_traffic) tools refuse `adjust_rps` and `set_rps` during a hold for that reason). Heartbeats and the `ack` carry `hold_rps`.

### `release`

*(`gg` v1.5+, capability `control.hold`.)* Ends a hold; the rate rejoins the profile where it is now.

```json
{"id":"c6","command":"release"}
```

Takes no arguments. A `gg` that does not list `control.hold` answers both with `unknown_command`.

### `autopilot`

*(`gg` v1.5+, capability `control.autopilot`.)* Starts or stops the built-in [autopilot](/autopilot) on a running run.

```json
{"id":"c7","command":"autopilot","action":"start","mode":"slo-guard","max_p99_ms":250,"max_error_rate":0.01}
{"id":"c8","command":"autopilot","action":"stop"}
```

`mode` is `slo-guard` or `find-ceiling`; the other fields are optional numbers that default as the [CLI flags](/autopilot#flags) do: `max_p99_ms`, `max_error_rate`, `min_samples`, `start_rps`, `step_pct`, `step_rps`, `step_interval_s`, `confirm_hold_s`, `max_rps`, `max_duration_s`, `recover_hold_s`. A bad value is an `invalid_argument` error.

`find-ceiling` owns the run's schedule, so it can only be (re)started on a run that was launched with `--autopilot find-ceiling`; any other run answers `invalid_argument`. While a controller owns the rate, `bias`, `hold` and `release` are refused with the error reason `autopilot_active`. `stop` on the run always works. `autopilot` `stop` leaves the rate where it was; use `release`, `hold` or `bias` next.

### `stop`

Ends the run cleanly: the snapshot is finalized exactly as for a run that completed on its own, and the process exits with status `0`. (`Ctrl+C` is different: it ends a run with an `interrupted` event.) If `--snap` is set, the snapshot is still written, including any marks and bias applied before the stop.

```json
{"id":"c4","command":"stop"}
```

Takes no arguments.

---

## 📬 Replies

Every command produces exactly one reply, interleaved with the regular heartbeat stream:

```json
{"time":"…","event":"ack","id":"c1","command":"bias","bias":15,"message":"bias +5 applied (cumulative +15)"}
{"time":"…","event":"error","id":"c2","command":"bias","reason":"invalid_argument","message":"amount must be a non-zero integer"}
{"time":"…","event":"mark","id":"c3","label":"deploy-v2-canary","elapsed_s":12.4}
```

- **`bias`** → `ack` on success (carries the cumulative value in the `bias` field, not just in `message`), `error` on a bad `amount`.
- **`mark`** → the `mark` event **is** the reply: there's no separate `ack`. Its `elapsed_s` is the offset the mark was recorded at (always present, including `0`), and `"clamped":true` appears only if your `elapsed_s` was capped.
- **`hold`**, **`release`** → `ack` (a hold's `ack` carries `hold_rps`; both echo `source`). A `bias`, `hold` or `release` sent while the built-in autopilot owns the rate is an `error` with the reason `autopilot_active`.
- **`autopilot`** → `ack` on success, then `autopilot` events as the controller works (see [Autopilot](#autopilot) below).
- **`stop`** → an immediate `ack` confirming receipt, then the run's terminal event fires once finalize completes: `stopped` (replacing `finished`) rather than a second reply. Like `finished`, it carries the final run statistics: also when `--snap` is on, in which case its `message` is the snapshot status line.

A malformed or unparseable line still gets a reply: with no `id`/`command`, since neither could be recovered:

```json
{"event":"error","reason":"parse_error","message":"invalid command line: …"}
```

| `reason` | When |
|---|---|
| `parse_error` | The line wasn't valid JSON at all. |
| `unknown_command` | Valid JSON, but `command` isn't one this `gg` accepts (check [`capabilities`](#versioning--capabilities)). |
| `invalid_argument` | Missing/zero `amount` on `bias`, `rps` below 1 on `hold`, empty `label` or a bad `elapsed_s` on `mark`, a bad `source`, or a bad `autopilot` field. |
| `autopilot_active` | A rate command arrived while the built-in autopilot owns the rate. |

Unrecognized extra fields on a command are ignored, not rejected: send whatever your plugin's data model already has lying around.

In `--reporter text` mode the same replies print as a human-readable line instead of JSON, e.g. `[time] mark "deploy-v2-canary" (t+12.4s)`.

---

## 💓 Heartbeat additions

The regular heartbeat stream gains one field: `bias` (the cumulative Director bias, `0` if nothing has been applied). The `bias` command's `ack` carries the same cumulative value; no other event does, so read the running total from `heartbeat`. Nothing existing is removed or renamed.

```json
{"time":"…","event":"heartbeat","stage":1,"total_stages":2,"target_rps":15,"actual_rps":14.8,"bias":5, …}
```

From `gg` v1.5.0 a heartbeat may also carry `error_mix`, a summary of *what kind* of failures happened in the last 10 seconds. It is there only when something failed in that window:

```json
{"time":"…","event":"heartbeat", …,
 "error_mix":{"window_s":10,"sampled":37,"transport":2,"http_4xx":30,"http_5xx":4,"extract":1,
              "top_codes":[{"code":429,"n":28},{"code":503,"n":4},{"code":404,"n":2}],"capped":false}}
```

| Field | Meaning |
|---|---|
| `window_s` | How far back it looks, in seconds. |
| `sampled` | Failures counted: `transport + http_4xx + http_5xx + extract`. |
| `transport` | No HTTP response at all (refused, reset, DNS, timeout). |
| `http_4xx` / `http_5xx` | Responses with a 4xx / 5xx status. |
| `extract` | Status was fine but an `@gg-export` found nothing, so the journey stopped. |
| `top_codes` | Up to three most frequent 4xx/5xx codes. Left out if there were none. |
| `capped` | `true` if the engine's bounded failure log filled up inside the window. Counts are then a lower bound; the mix between kinds is still a good guide. |

From v1.5 `error_mix` also carries **`top_endpoints`**, the three most-failing endpoints in that window, grouped by the request spec rather than the resolved URL, so failures on `/users/1` through `/users/500` from one `GET /users/{{id}}` count as one endpoint:

```json
"top_endpoints":[{"endpoint":"POST /orders","n":28,"code":503,"kind":"http_5xx"}]
```

`kind` is the dominant failure kind (`transport`, `http_4xx`, `http_5xx` or `extract`) and `code` the dominant status, or `0` for transport and extraction failures.

It is a "what is going wrong right now" view, not a running total (use `failure_count` and `error_rate` for those). It needs no capability check: a client that doesn't know the field just ignores it. The text reporter does not print it.

### v1.5: windowed health, time and the pinned rate

Also from v1.5, heartbeats (and the `ack`, `mark`, `stopped` and `finished` events) carry **`elapsed_s`**, seconds since the run started, so a client no longer has to keep its own clock. Heartbeats add:

```json
{"event":"heartbeat", …, "elapsed_s":42.0, "hold_rps":120,
 "window":{"seconds":5,"p99_ms":38.2,"error_rate":0.0,"samples":610},
 "autopilot":{"mode":"find-ceiling","state":"stepping"}}
```

| Field | Meaning |
|---|---|
| `window` | Health over the last few seconds: p99, error rate (failed over **completed** requests, so in-flight ones do not dilute it), and the number of latency samples behind the p99. `seconds` is the span actually covered, which is less than the full window early in a run. Unlike the cumulative figures it shows a recovery or a breach as it happens. `capped` is `true` if the window held more samples than were read. |
| `hold_rps` | The pinned rate, only while a hold is active. |
| `autopilot` | The controller's `mode` and `state`, only while one runs. |

See [CLI Reference → Headless heartbeat payload](/cli-reference#headless-heartbeat-payload) for the full field list.

---

## Versioning & capabilities

The `started` event gains two fields so old and new binaries/plugins can talk to each other without guessing versions:

```json
{"event":"started","protocol_version":1,"capabilities":["control.bias","control.mark","control.mark.elapsed","control.stop","control.hold","control.autopilot"], …}
```

- **`protocol_version`** bumps only on a breaking change (the goal is to never need this).
- **`capabilities`** lists what this run's listener actually accepts. Any command added in the future would show up as a new capability string: check for the string, not the version number, before showing a control in your own tooling.
- **`control.mark.elapsed`** means `mark` honors its optional `elapsed_s`. Check that the list *contains* the string; don't compare the whole list, since it grows over time.
- **`control.hold`** means `hold` and `release` work. **`control.autopilot`** means the `autopilot` command works. Neither is advertised by an older `gg`.
- With `--control none`, `capabilities` is an explicit empty array (`[]`): distinct from an older `gg` binary that has no `capabilities` field at all.

A tool built against v1.3 that talks to a newer `gg` with extra capabilities keeps working unchanged; it just won't offer controls for capabilities it doesn't recognize.

---

## 🖱 For plugin authors: button vs. slider

The protocol carries a generic signed `amount` on purpose: the widget is a plugin decision, not a protocol one. Our recommendation: build a **stepper (± buttons)**, matching the TUI's feel and giving users exact parity with what they already know from the terminal. The JetBrains plugin does this, with −10 / −5 / +5 / +10 steps and a Reset.

A slider is a perfectly reasonable follow-up using the exact same `bias` command: just **debounce drag events**. Every command is applied and none are dropped, but the request rate only changes once a second, so one command per pixel of drag adds noise (and a long reply stream) without adding precision. Emit on drag-end, or throttle to a few times a second.

---

## 🕒 For plugin authors: marks with a confirm step

If your UI asks for a label after the user clicks "mark", the moment that matters is the click, not the confirm. Record the offset at click time (seconds since you received `started`, from a monotonic clock), hold it while the dialog is open, and send it as `elapsed_s` with the command: but only when `capabilities` contains `control.mark.elapsed`; otherwise send the mark without it. Marks can then arrive out of order (an early click confirmed after a later one); the snapshot lists them by `elapsed_s`, so the order in the file is chronological. If the run ends while the dialog is open there's nothing left to receive the command, so tell the user the mark wasn't saved.

---

## Autopilot

When the built-in [autopilot](/autopilot) runs, gg emits an `autopilot` event at each decision, and heartbeats carry its status:

```json
{"event":"autopilot","mode":"find-ceiling","state":"breached","hold_rps":118,"elapsed_s":30,
 "reason":"ceiling breached at 148 RPS (p99=402ms > 200ms); backing off to 118 RPS"}
{"event":"autopilot","mode":"find-ceiling","state":"done","outcome":"ceiling_found","ceiling_rps":118,"reason":"ceiling confirmed at 118 RPS"}
```

- **`state`** is one of `stepping`, `breached`, `confirming` (find-ceiling), `clamped`, `releasing`, `watching` (the guard's idle state) or `done`.
- **`outcome`**, on the last event, is `ceiling_found`, `ceiling_not_reached`, `unstable`, `stopped` or `completed` (the guard ran to the end of the run).
- The terminal `finished` or `stopped` event repeats `mode`, `outcome` and `ceiling_rps`.
- The controller drops `mark` events with `source: "autopilot"` when it breaches and when it confirms, so they land in the snapshot.

---

## The IDE bridge

*(`gg` v1.5+, for plugin authors.)* A plugin does not have to spawn a `gg --headless` process per run and parse its stdout. It can run one long-lived `gg mcp --port 0 --exit-on-stdin-eof` daemon and speak LCP to it over HTTP. The same daemon serves the [MCP tools](/mcp), so a run an agent starts appears in the plugin's dashboard, and a run the plugin starts is visible to the agent. The commands and events are the protocol above, unchanged; the bridge only adds a session envelope. Everything sits behind the daemon's token and loopback checks.

**Starting the daemon.** On start it prints one JSON line on stdout, `{"event":"listening","url":"http://127.0.0.1:PORT/mcp","pid":N}`, and records the URL, token and pid in a per-workspace discovery file under the user cache directory (mode `0600`). Closing the daemon's stdin stops every run and exits, so it cannot outlive the IDE.

**`GET /plugin/events`** is a server-sent-events stream of every run. Each message's data is:

```json
{"session_id":"sim_b6f013af50e6","origin":"agent","autopilot":"find-ceiling","lcp":{"event":"heartbeat", …}}
```

`lcp` is the child's line exactly as `gg --headless` printed it. On connect you are caught up per run with `started`, the last 50 marks, the latest `autopilot` event and the latest heartbeat, so a plugin that opens late or reconnects sees the current state. Beyond these gg adds a `failed` event (the process died without a result, with the reason) and an `ack` with `command: "agent_steer"`. A slow reader first loses its oldest heartbeats and receives a `{"event":"lagged","dropped":N}` notice; if it stops reading altogether the stream is cut with an `error` event so it reconnects and replays. A keep-alive comment is sent every 15 seconds.

**`POST /plugin/start`** starts a run as `origin: "user"`. The body is `start_simulation`'s arguments, plus `config_path` (run a config file as it is) and `allow_agent_steer`. A user run is exempt from the server's `--max-rps`, `--max-duration` and `--allow-host`; the agent caps apply to agent runs only. It returns the run's `session_id`.

**`POST /plugin/control`** takes one LCP command object plus a `session_id`, applies it as `source: "user"`, and returns gg's own `ack` or `error`, with your `id`. `agent_steer` with `"allow": true|false` is the one command the daemon answers itself: it opens a user run to agent steering, or closes it again.

| Status | When |
|---|---|
| `200` | gg replied (`ack` or `error`). |
| `400` | Bad body or unknown command. |
| `404` | Unknown session. |
| `409` | `session_not_running`, or `session_limit` on start. |
| `422` | `start_failed`. |
| `502` | `no_reply`. |

---

## 🎬 Scripted game days

Because the listener just reads NDJSON off stdin, you don't need a plugin to script a run. One thing to know first: **a file piped with `<` is consumed immediately**, so every line is applied at the start of the run: the format has no timestamps.

```bash
gg config.yaml --headless < gameday-script.jsonl     # all commands at t=0
```

To space commands out over time, feed them from a subshell with `sleep`s:

```bash
(sleep 30; echo '{"command":"bias","amount":50}'
 sleep 60; echo '{"command":"mark","label":"flag flipped"}'
 sleep 60; echo '{"command":"stop"}') | gg config.yaml --headless --reporter json --snap
```

Combine it with `--snap` and the resulting snapshot carries every mark and bias event you sent, so a later `gg snap diff` can tell a real regression apart from a human leaning on the RPS slider. For the multi-terminal and container recipes, see [Driving a Run Live](/live-control).
