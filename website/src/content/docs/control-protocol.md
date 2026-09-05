---
title: "Live Control Protocol"
description: "Drive a running gg process from stdin — bias, mark, and stop a headless load test without killing it."
---

Headless mode used to be a one-way radio: `gg --headless` gave you a live heartbeat stream, but the only way to touch it was `Ctrl+C`. The **Live Control Protocol (LCP)** turns that into a two-way conversation — send commands in over stdin, get replies interleaved with the heartbeats out.

This is the foundation live bias/mark/stop controls in the [JetBrains and VS Code plugins](/plugin) will build on. It's also directly scriptable today, no IDE required: pipe a file of commands at a headless run for scripted game days.

> **v1.3 scope:** `bias`, `mark`, `stop`. More commands (`pause`/`resume`, `set-rps`, chaos toggles) are coming in later releases — see [Roadmap](https://github.com/shyam-s00/gopher-glide) for what's next. The protocol is versioned so old and new binaries/plugins stay compatible as that grows (see [Versioning & capabilities](#versioning--capabilities) below).

---

## 🔌 Enabling it

The listener is on by default in headless mode:

```bash
gg config.yaml --headless --reporter json
```

Type (or pipe) a command as a single line of JSON and press enter. To disable the listener entirely — locked-down CI, or a shell that forwards stdin somewhere else — pass `--control none`:

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

`amount` must be a non-zero integer (positive or negative). The bias is cumulative — three `+5` commands add up to `+15`, exactly like pressing `↑` three times.

### `mark`

Drops a labeled, timestamped annotation into the run — useful for narrating a game day ("canary deploy started here") so it shows up later when you diff the resulting snapshot.

```json
{"id":"c3","command":"mark","label":"deploy-v2-canary"}
```

`label` must be a non-empty string.

### `stop`

Ends the run cleanly and immediately — same finalize path as `Ctrl+C`, but without killing the process. If `--snap` is set, the snapshot is still written, including any marks and bias applied before the stop.

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
- **`stop`** → an immediate `ack` confirming receipt, then the run's terminal event fires once finalize completes: `stopped` (replacing `finished`) rather than a second reply.

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

The regular heartbeat stream gains one field: `bias` (the cumulative Director bias, same value the `bias` command's `ack` reports). Nothing existing is removed or renamed.

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
- **`capabilities`** lists what this run's listener actually accepts. New commands show up as new capability strings in later releases — check for the string, not the version number, before showing a control in your own tooling.
- With `--control none`, `capabilities` is an explicit empty array (`[]`) — distinct from an older `gg` binary that has no `capabilities` field at all.

A tool built against v1.3 that talks to a future `gg` with more capabilities keeps working unchanged; it just won't offer controls for capabilities it doesn't recognize yet.

---

## 🖱 For plugin authors: button vs. slider

The protocol carries a generic signed `amount` on purpose — the widget is a plugin decision, not a protocol one. Our recommendation for v1.3: build a **stepper (± buttons)**, matching the TUI's feel and giving users exact parity with what they already know from the terminal.

A slider is a perfectly reasonable follow-up using the exact same `bias` command — just **debounce drag events**. The engine drains bias commands about once a second; flooding it with one command per pixel of drag will silently drop most of them and feel unresponsive. Emit on drag-end, or throttle to a few times a second.

---

## 🎬 Scripted game days

Because the listener just reads NDJSON off stdin, you don't need a plugin at all to script a run:

```bash
gg config.yaml --headless < gameday-script.jsonl
```

Where `gameday-script.jsonl` is any file of one command per line — bias up before a load spike, mark the moment you flip a feature flag, `stop` when you're done. Combine it with `--snap` and the resulting snapshot carries every mark and bias event you sent, so a later `gg snap diff` can tell a real regression apart from a human leaning on the RPS slider.
