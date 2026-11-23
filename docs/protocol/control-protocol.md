# Live Control Protocol

**Status:** Supported from gg v1.3.0+. The optional `elapsed_s` field on `mark` is supported from gg v1.4.0+.

This document specifies the Live Control Protocol (LCP) — a way to send commands into a running
`gg` headless load test, and to receive structured replies and events back. It is a complete,
self-contained reference: everything a client (an IDE plugin, a CI script, a person piping JSON
by hand) needs to know to talk to `gg` over this protocol, with no assumed knowledge of `gg`'s
internals.

If you're integrating with `gg` — building a plugin, writing a control script, or just want to
understand the wire format — this is the document to read.

---

## 1. Why this exists

`gg --headless` runs a load test and streams structured progress ("heartbeat") events to
stdout as it goes. Until this protocol, that stream was one-way: you could watch a run, but you
couldn't influence it without killing the process and starting over. The Live Control Protocol
adds a second channel — commands sent to `gg` on stdin — so a running test can be nudged,
annotated, and stopped cleanly, in real time, without interrupting it.

The initial version of this protocol supports three things:

- **`bias`** — nudge the request rate up or down on top of whatever the load profile is already
  doing.
- **`mark`** — drop a timestamped annotation into the run (e.g. "starting canary deploy") for
  later reference.
- **`stop`** — end the run gracefully, with a clean exit and a finalized result, instead of
  killing the process.

More commands are expected to arrive in later versions of `gg` — see
[Versioning and Forward Compatibility](#8-versioning-and-forward-compatibility) for how that
happens without breaking existing clients.

---

## 2. Transport

- **Commands in:** newline-delimited JSON (NDJSON) on **stdin** — one JSON object per line.
  This channel is only read when `gg` is run with `--headless` and control is enabled
  (`--control stdin`, the default in headless mode; `--control none` disables it).
- **Replies and events out:** the existing stdout heartbeat stream (JSON-lines, one object per
  line, when run with `--reporter json`) gains new event types alongside the ones that already
  exist (`started`, `heartbeat`, `finished`, `snap`, `interrupted`, `error`). This protocol adds:
  `ack`, `mark`, `stopped`, and extends the existing `error` event with structured detail.
- The interactive terminal UI (the non-headless, default mode of `gg`) is not affected by this
  protocol — it has its own keyboard-driven controls.
- Text-format output (`--reporter text`) receives the same information rendered as
  human-readable lines rather than JSON.

A client that never writes to stdin sees no change in behavior. A client that writes malformed
or unrecognized input gets a structured error back, never a crash.

---

## 3. Commands

Every command is a single JSON object, one per line.

```json
{"id":"c1","command":"bias","amount":5}
```

### Common fields

| Field | Type | Required | Meaning |
|---|---|---|---|
| `command` | string | yes | Which command this is. |
| `id` | string | no | Caller-chosen identifier, echoed back in the reply so a client can match replies to the commands that produced them. Safe to omit for one-off, unscripted use. |

Fields not recognized by a given command are ignored, not rejected — see
[Versioning and Forward Compatibility](#8-versioning-and-forward-compatibility).

### 3.1 `bias`

Applies a delta to the run's target request rate, on top of whatever the load profile is
already doing. The delta is cumulative: sending `bias` twice with `amount: 5` results in a total
bias of `+10`, not `+5`.

```json
{"id":"c1","command":"bias","amount":5}
{"id":"c2","command":"bias","amount":-3}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `amount` | integer | yes | Signed, non-zero. Positive increases the target rate, negative decreases it. There is no fixed step size and no upper or lower bound enforced by the protocol. |

On success, replies with an [`ack`](#41-ack) whose `bias` field carries the new cumulative value.
On failure — `amount` missing, zero, or not an integer — replies with an [`error`](#42-error),
reason `invalid_argument`.

### 3.2 `mark`

Drops a labeled, timestamped annotation into the run. Useful for correlating something that
happened during the test (a deploy, a config change, an external event) with the run's metrics
after the fact.

```json
{"id":"c3","command":"mark","label":"deploy-v2-canary"}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `label` | string | yes | Non-empty free text describing the annotation. |
| `elapsed_s` | number | no | Seconds since the run started at which the annotation belongs. For clients that capture the moment before they can send the command — for example a UI that asks for a label after the user clicks. Must be a non-negative number; `null` counts as omitted. Default: the moment `gg` receives the command. |

```json
{"id":"c3","command":"mark","label":"deploy-v2-canary","elapsed_s":12.4}
```

Measure `elapsed_s` from when the client received the `started` event; `gg` begins its own clock
a moment earlier, so expect a difference of milliseconds. A value later than the run's current
elapsed time can't be right, so it is capped to the current time and the reply carries
`"clamped":true` — the mark is still recorded. Only a `gg` that advertises the
[`control.mark.elapsed`](#5-handshake) capability honors the field; an older one ignores it and
stamps the mark with its receive time, without an error. On other commands the field is ignored.

On success, the reply *is* a [`mark` event](#43-mark) — there is no separate acknowledgment. On
failure — `label` missing or empty, or `elapsed_s` present but not a non-negative number —
replies with an `error`, reason `invalid_argument`.

### 3.3 `stop`

Ends the run gracefully: results are finalized exactly as they would be for a run that
completed on its own, the process exits with status code `0`, and the stream terminates with a
single `stopped` event rather than `finished`.

```json
{"id":"c4","command":"stop"}
```

Takes no arguments. Replies with an [`ack`](#41-ack) immediately, confirming the command was
received; the run then winds down and the terminal `stopped` event follows once results are
finalized. That terminal event is not a second reply to the command — it's the run ending, which
`stop` merely set in motion.

---

## 4. Replies and events

Every command produces **exactly one** reply. What kind of reply depends on the command and on
whether it succeeded:

| Command | On success | On failure |
|---|---|---|
| `bias` | `ack` | `error` |
| `mark` | `mark` (the event itself is the reply) | `error` |
| `stop` | `ack` (immediate); the run's terminal `stopped` event follows separately | `error` |

### 4.1 `ack`

```json
{"time":"2026-08-31T12:00:05Z","event":"ack","id":"c1","command":"bias","bias":5,"message":"bias +5 applied (cumulative +5)"}
```

| Field | Meaning |
|---|---|
| `id` | Echoes the command's `id`, if one was given. |
| `command` | Which command this acknowledges. |
| `bias` | Present on `bias` command acks only: the cumulative bias value *including* the command just applied. The engine folds it into the target rate at the start of its next 1-second window, so the effect shows up on the following `heartbeat`. Not present on any other event — read the running value from `heartbeat`. |
| `message` | Human-readable summary. Not intended to be machine-parsed — use the structured fields instead. |

### 4.2 `error`

```json
{"time":"2026-08-31T12:00:06Z","event":"error","id":"c2","command":"bias","reason":"invalid_argument","message":"amount must be a non-zero integer"}
```

| Field | Meaning |
|---|---|
| `id` | Echoes the command's `id`, when the line parsed far enough to have one. Absent for lines that weren't valid JSON at all. |
| `command` | Which command failed. Absent for lines that weren't valid JSON at all. |
| `reason` | A stable, machine-checkable error code — see the table below. |
| `message` | Human-readable detail. |

**Error reasons:**

| Reason | Meaning |
|---|---|
| `parse_error` | The line wasn't valid JSON at all. |
| `unknown_command` | The line was valid JSON, but `command` wasn't recognized. |
| `invalid_argument` | The command was recognized, but its arguments failed validation (e.g. a `bias` of `0`, or an empty `mark` label). |

This list only grows over time — an existing reason will never be repurposed to mean something
different, and a client can safely fall back to displaying `message` for any reason it doesn't
specifically handle.

### 4.3 `mark`

```json
{"time":"2026-08-31T12:00:10Z","event":"mark","id":"c3","label":"deploy-v2-canary","elapsed_s":12.4}
```

| Field | Meaning |
|---|---|
| `id` | Echoes the command's `id`, if one was given. |
| `label` | The annotation text, as sent. |
| `elapsed_s` | The offset the mark was recorded at, in seconds since the run started: the `elapsed_s` the client sent (possibly capped — see `clamped`), or, if none was sent, the time `gg` received the command. Always present on `mark` events, including `0`. |
| `clamped` | `true` only when the client's `elapsed_s` was later than the run's current time and was capped to it. Omitted otherwise. |

### 4.4 `stopped`

```json
{"time":"2026-08-31T12:01:00Z","event":"stopped","total_requests":48213,"success_count":48100,"failure_count":113,"error_rate":0.0023,"p50_ms":42.1,"p95_ms":118.7,"p99_ms":210.3}
```

The run's terminal event when ended via `stop`, in place of `finished`. Carries the same final
run statistics `finished` would have (always — also when `--snap` is on, in which case `message` is the snapshot status line). A stream that ends in `stopped` and one that ends in
`finished` both represent a completed, valid run — the difference is only *why* it ended.

---

## 5. Handshake

The first line `gg --headless` ever writes (the `started` event) includes two fields that
establish what this run of `gg` supports:

```json
{"event":"started","protocol_version":1,"capabilities":["control.bias","control.mark","control.mark.elapsed","control.stop"], "...": "..."}
```

| Field | Meaning |
|---|---|
| `protocol_version` | An integer, starting at `1`. Only increments on a breaking change to this protocol (expected to be rare — see [Versioning and Forward Compatibility](#8-versioning-and-forward-compatibility)). |
| `capabilities` | The list of control commands this specific run actually accepts, as namespaced strings (`control.bias`, `control.mark`, `control.mark.elapsed`, `control.stop`, ...). |

**A client should check for the specific capability string it needs, not assume support based on
`protocol_version` alone.** For example, before showing a bias control in a UI, check that
`"control.bias"` is present in `capabilities`.

`control.mark.elapsed` means the `mark` command honors its optional `elapsed_s` field. It is a
separate string because a `gg` without it silently ignores `elapsed_s` and stamps marks with the
receive time. Check for it, and when it's absent send `mark` without `elapsed_s`. Check by
membership (`capabilities` contains the string), never by comparing the whole list.

If control is disabled for the run (`--control none`), `capabilities` is present but **empty**
(`[]`) — this is a deliberate, meaningful signal: "this binary supports the protocol, but this
particular run has it turned off," which a client should render as a read-only view. This is
different from the field being **absent** entirely, which means the binary predates this
protocol and doesn't know about it at all — also a read-only view, but for a different reason.
Either way, no `capabilities` (or an empty one) means: don't send commands, this run won't act
on them.

---

## 6. Heartbeat additions

Every `heartbeat` line gains one new field, and (since `gg` v1.4.0) may carry a second:

```json
{"event":"heartbeat","bias":5, "...": "..."}
```

| Field | Meaning |
|---|---|
| `bias` | The cumulative bias currently in effect, from all `bias` commands sent so far in this run. `0` if none has been sent. Always present on heartbeats, not just when non-zero. |
| `error_mix` | What kind of failures happened recently. Present on `heartbeat` events only, and only when at least one request failed in the last 10 seconds; absent otherwise (and on every other event). See below. |

### 6.1 `error_mix`

```json
{"event":"heartbeat", "...": "...",
 "error_mix":{"window_s":10,"sampled":37,"transport":2,"http_4xx":30,"http_5xx":4,"extract":1,
              "top_codes":[{"code":429,"n":28},{"code":503,"n":4},{"code":404,"n":2}],"capped":false}}
```

| Field | Meaning |
|---|---|
| `window_s` | How far back the summary looks, in seconds (currently 10). |
| `sampled` | Failed requests counted. Always `transport + http_4xx + http_5xx + extract`. |
| `transport` | No HTTP response: connection refused, reset, DNS, timeout. |
| `http_4xx`, `http_5xx` | Responses with a 4xx / 5xx status. |
| `extract` | The response was fine (status below 400) but an `@gg-export` found nothing, so the journey was aborted. |
| `top_codes` | Up to three most frequent 4xx/5xx status codes, most frequent first (ties: lower code first). Omitted when there were none. |
| `capped` | `true` when the engine's bounded failure log filled up inside the window, so older failures in the window may be missing. Counts are then a lower bound; the proportions between kinds remain meaningful. |

It is a summary of recent failures, not a running total: use `failure_count` and `error_rate` for
totals. There is no capability string for it, because it is purely additive; a client that does
not know the field ignores it. Engines that cannot produce it simply never send it.

---

## 7. Interaction with snapshots

`gg` can optionally capture a behavioral snapshot of a run (`--snap`). When it does, the
snapshot also records what control activity happened during that run:

| Field | Meaning |
|---|---|
| `marks` | The list of `mark` commands sent during the run, each with its label and the elapsed time (seconds since run start) it belongs to — the client's `elapsed_s` when one was sent, otherwise the time `gg` received it. Listed in order of elapsed time, which can differ from the order received. Omitted if no marks were sent. |
| `bias_events` | The list of `bias` commands sent during the run, each with its amount, the resulting cumulative value, and elapsed time. Omitted if bias was never touched. |
| `final_bias` | The cumulative bias value at the end of the run. `0` if bias was never touched. Always present, even at `0`. |

This exists so that comparing two snapshots later — to see whether a change in throughput or
latency is a real regression — doesn't silently mistake a manual bias adjustment made during one
of the runs for a change in the system under test. If a snapshot shows non-zero `final_bias` or a
populated `bias_events` list, that run was manually influenced and shouldn't be treated as a
clean baseline.

Control activity that happens during a run **without** `--snap` enabled is still visible live on
the stream (see [Replies and Events](#4-replies-and-events)) — it simply isn't written anywhere afterward.

---

## 8. Versioning and forward compatibility

This protocol is designed so that old and new versions of `gg` and its clients keep working
together as the protocol grows:

1. **New fields are always additive.** A client should ignore any JSON field it doesn't
   recognize, rather than rejecting the line. Existing fields never change their meaning.
2. **New commands arrive as new capability strings**, checked individually (see [Handshake](#5-handshake)) — a client
   never needs to know the full list in advance, only the ones it cares about.
3. **`protocol_version` only increments on a breaking change** — something that isn't just an
   addition, but changes the meaning of something that already existed. This is intended to be
   rare.
4. **A client with no knowledge of this protocol at all continues to work unmodified.** It
   simply never sees a `capabilities` field, never sends any commands, and the run behaves
   exactly as it always did.

**Capability namespaces:** capability strings are namespaced by area — `control.*` for
run-control commands like the ones in this document. Any new capability is additive and does
not require a protocol version bump.

**Practical guidance for high-frequency use cases:** commands are processed in the order they're
received. A UI element that can generate commands very rapidly (a slider, for example, as
opposed to a discrete button) should debounce its output rather than sending one command per UI
event — sending a burst of `bias` commands faster than the server can act on them will make the
UI feel unresponsive without adding any real precision.