---
title: "Live Control Protocol"
description: "Drive a running gg process from stdin — bias, mark, and stop a headless load test without killing it."
---

Headless mode used to be a one-way radio: `gg --headless` gave you a live heartbeat stream, but the only way to touch it was `Ctrl+C`. The **Live Control Protocol (LCP)** turns that into a two-way conversation — send commands in over stdin, get replies interleaved with the heartbeats out.

This is what the live bias/mark/stop controls in the [JetBrains plugin](/plugin#-native-run-dashboard) (7.0+) are built on; the VS Code extension is next. It's also directly usable,  no IDE required — type commands into a headless run, or script them. **New to this? Start with [Driving a Run Live](/live-control)**, a hands-on guide with a copy-paste example, the multi-terminal and container recipes, and troubleshooting. This page is the reference.

> **Commands today:** `bias`, `mark`, `stop`. The protocol is versioned and capability-based, so if commands are ever added, existing binaries and plugins keep working (see [Versioning & capabilities](#versioning--capabilities) below).

---

## 🔌 Enabling it

The listener is on by default in headless mode:

```bash
gg config.yaml --headless --reporter json
```

Type (or pipe) a command as a single line of JSON and press enter. Commands are read from **the standard input of the `gg` process itself** — the terminal or pipe it was started in — so a command typed in a different terminal tab doesn't reach it (see [Where do commands go?](/live-control#-where-do-commands-go)). If stdin closes, the run is not affected: end-of-input is not a `stop`. To disable the listener entirely — locked-down CI, or a shell that forwards stdin somewhere else — pass `--control none`:

```bash
gg config.yaml --headless --control none
```

| Flag | Default | Description |
|---|---|---|
| `--control <stdin\|none>` | `stdin` | `stdin` listens for commands on stdin. `none` disables the listener; the run stays fully read-only, same as pre-v1.3 behavior. |

TUI mode is unaffected — the TUI already has `↑`/`↓` for bias (see [Director Mode](/tui#director-mode)); it doesn't gain a stdin listener.

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
| `command` | `"bias"`, `"mark"`, or `"stop"`. |

### `bias`

Applies a signed delta on top of the current stage's target RPS — the same mechanism as the TUI's `↑`/`↓` keys, just with an arbitrary amount instead of a fixed ±5.

```json
{"id":"c1","command":"bias","amount":5}
```

`amount` must be a non-zero integer (positive or negative). The bias is cumulative — three `+5` commands add up to `+15`, exactly like pressing `↑` three times — and it rides on top of whatever the load profile is doing at that moment. No upper or lower bound is enforced by the protocol, but the effective rate never drops below 1 RPS.

The `ack` comes back immediately and already includes the new cumulative value. The request rate itself changes at the next one-second window, so the next heartbeat or two shows the effect.

### `mark`

Drops a labeled, timestamped annotation into the run — useful for narrating a game day ("canary deploy started here") so it shows up later when you diff the resulting snapshot.

```json
{"id":"c3","command":"mark","label":"deploy-v2-canary"}
```

`label` must be a non-empty string.

### `stop`

Ends the run cleanly — the snapshot is finalized exactly as for a run that completed on its own, and the process exits with status `0`. (`Ctrl+C` is different: it ends a run with an `interrupted` event.) If `--snap` is set, the snapshot is still written, including any marks and bias applied before the stop.

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
- **`mark`** → the `mark` event **is** the reply — there's no separate `ack`.
- **`stop`** → an immediate `ack` confirming receipt, then the run's terminal event fires once finalize completes: `stopped` (replacing `finished`) rather than a second reply. Like `finished`, it carries the final run statistics — also when `--snap` is on, in which case its `message` is the snapshot status line.

A malformed or unparseable line still gets a reply — with no `id`/`command`, since neither could be recovered:

```json
{"event":"error","reason":"parse_error","message":"invalid command line: …"}
```

| `reason` | When |
|---|---|
| `parse_error` | The line wasn't valid JSON at all. |
| `unknown_command` | Valid JSON, but `command` isn't one of `bias` / `mark` / `stop`. |
| `invalid_argument` | Missing/zero `amount` on `bias`, or empty `label` on `mark`. |

Unrecognized extra fields on a command are ignored, not rejected — send whatever your plugin's data model already has lying around.

In `--reporter text` mode the same replies print as a human-readable line instead of JSON, e.g. `[time] mark "deploy-v2-canary" (t+12.4s)`.

---

## 💓 Heartbeat additions

The regular heartbeat stream gains one field: `bias` (the cumulative Director bias, `0` if nothing has been applied). The `bias` command's `ack` carries the same cumulative value; no other event does, so read the running total from `heartbeat`. Nothing existing is removed or renamed.

```json
{"time":"…","event":"heartbeat","stage":1,"total_stages":2,"target_rps":15,"actual_rps":14.8,"bias":5, …}
```

See [CLI Reference → Headless heartbeat payload](/cli-reference#headless-heartbeat-payload) for the full field list.

---

## Versioning & capabilities

The `started` event gains two fields so old and new binaries/plugins can talk to each other without guessing versions:

```json
{"event":"started","protocol_version":1,"capabilities":["control.bias","control.mark","control.stop"], …}
```

- **`protocol_version`** bumps only on a breaking change (the goal is to never need this).
- **`capabilities`** lists what this run's listener actually accepts. Any command added in the future would show up as a new capability string — check for the string, not the version number, before showing a control in your own tooling.
- With `--control none`, `capabilities` is an explicit empty array (`[]`) — distinct from an older `gg` binary that has no `capabilities` field at all.

A tool built against v1.3 that talks to a newer `gg` with extra capabilities keeps working unchanged; it just won't offer controls for capabilities it doesn't recognize.

---

## 🖱 For plugin authors: button vs. slider

The protocol carries a generic signed `amount` on purpose — the widget is a plugin decision, not a protocol one. Our recommendation: build a **stepper (± buttons)**, matching the TUI's feel and giving users exact parity with what they already know from the terminal. The JetBrains plugin does this, with −10 / −5 / +5 / +10 steps and a Reset.

A slider is a perfectly reasonable follow-up using the exact same `bias` command — just **debounce drag events**. Every command is applied and none are dropped, but the request rate only changes once a second, so one command per pixel of drag adds noise (and a long reply stream) without adding precision. Emit on drag-end, or throttle to a few times a second.

---

## 🎬 Scripted game days

Because the listener just reads NDJSON off stdin, you don't need a plugin to script a run. One thing to know first: **a file piped with `<` is consumed immediately**, so every line is applied at the start of the run — the format has no timestamps.

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
