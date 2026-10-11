---
title: "Driving a Run Live"
description: "Change the load while a test is running — arrow keys in the TUI, or typed, piped and scripted commands in headless mode, from a second terminal, or inside a container."
---

A load test doesn't have to be a fire-and-forget script. While a run is in progress you can **push the request rate up or down**, **drop a labeled marker** ("flipped the feature flag here"), and **stop it cleanly** — without restarting anything. Everything you do is recorded in the run's [snapshot](/snap#manual-bias--marks-in-the-raw-file), so a later `gg snap diff` can tell a real regression from a human leaning on the throttle.

| | In the TUI | Headless (`--headless`) |
|---|---|---|
| **Bias** the RPS | `↑` `↓` (±5 per press) | `{"command":"bias","amount":N}` |
| **Mark** a moment | — | `{"command":"mark","label":"…"}` |
| **Stop** cleanly | `q` | `{"command":"stop"}` |

The headless side is the [Live Control Protocol](/control-protocol) (LCP, available from gg v1.3). This page is the hands-on guide; the protocol page is the reference for plugin authors and scripts.

> **Bias is added on top of the profile, not instead of it.** If the plan says 50 RPS and you send `+30`, you get 80 RPS — and when the profile ramps, the bias rides along. It's cumulative (`+30` then `-20` leaves `+10`), has no upper limit, and the effective rate never drops below 1 RPS.

---

## ⚡ Try it in two minutes

You need a `.http` file and something to aim at. This runs the built-in `load` profile at a **50 RPS peak for 40 seconds**, applies a bias at 8 s, drops a mark at 14 s, adjusts the bias again at 19 s, and stops the run at 25 s:

```bash
(sleep 8;  echo '{"id":"b1","command":"bias","amount":30}'
 sleep 6;  echo '{"id":"m1","command":"mark","label":"deploy-v2"}'
 sleep 5;  echo '{"id":"b2","command":"bias","amount":-20}'
 sleep 6;  echo '{"id":"s1","command":"stop"}'
 sleep 3) \
| gg --profile load --peak-rps 50 --duration 40s --http-file target.http \
     --headless --reporter json --heartbeat-interval 1s \
     --snap --snap-dir ./snaps
```

The three flags that shape the run are `--profile` (the traffic shape), `--peak-rps` / `-r` (how high it goes) and `--duration` / `-d` (how long it takes) — see [Load Profiles](/profiles). Run `gg profile list` to browse the built-ins.

What you should see in the JSON stream:

| When | What to look for |
|---|---|
| Before 8 s | Heartbeats with `"bias":0`, `target_rps` ramping with the profile. |
| After `b1` | `{"event":"ack","id":"b1","command":"bias","bias":30,"message":"bias +30 applied (cumulative +30)"}`, then within about a second the heartbeats show `"bias":30` and `target_rps` 30 higher. |
| `m1` | One `{"event":"mark","id":"m1","label":"deploy-v2","elapsed_s":…}` — the mark event *is* the reply; there's no separate `ack`. |
| After `b2` | `ack` with `"bias":10`; `target_rps` drops by 20. |
| `s1` | An `ack` saying `stop received`, then a single `stopped` event. The process exits with code `0`. |

Then check what was recorded:

```bash
jq -c '.meta | {final_bias, marks, bias_events}' snaps/*.snap
```

Expect something like this (timings will differ):

```json
{"final_bias":10,"marks":[{"label":"deploy-v2","elapsed_s":14.0}],"bias_events":[{"amount":30,"cumulative":30,"elapsed_s":8.0},{"amount":-20,"cumulative":10,"elapsed_s":19.0}]}
```

---

## 🎯 Where do commands go?

This is the part that trips people up. **A `gg` process reads commands from its own standard input** — the terminal (or pipe) it was started in. It has no network port and no side door. So:

> A command typed, `echo`'d or piped in a *different* terminal tab never reaches the run. That tab's shell receives it, not `gg`.

Pick the way that matches how you want to drive the run:

| You want to… | Do this |
|---|---|
| Nudge a run by hand, with live visuals | Use the **TUI** and press `↑` / `↓` in the terminal that's running it. See [Director Mode](/tui#director-mode). |
| Type commands into a headless run | Type JSON lines **in the same terminal** that started `gg`, and press Enter. |
| Send commands from **another terminal or tab** | Connect them with a [named pipe](#-from-a-second-terminal). |
| Run a **timed** sequence | Use a [subshell with `sleep`s](#-timed-scripts), as in the quick start above. |
| Drive a run in a **container** | Pass [`-i` to `docker run`](#-in-a-container). |
| Drive it from a **plugin or your own tool** | Spawn `gg --headless` with a piped stdin and write one JSON line per command — see the [protocol reference](/control-protocol). |

### 📟 In the same terminal

```bash
gg --profile load --peak-rps 50 --duration 2m --http-file target.http --headless --reporter json --heartbeat-interval 1s
```

Now just type, with each command on one line:

```json
{"command":"bias","amount":20}
{"command":"mark","label":"cache warmed"}
{"command":"stop"}
```

The input has to be JSON. Plain text like `bias 20` is answered with `invalid command line: …` — an `error` event (`reason: parse_error`) — and in the default text output that single line is easy to lose between heartbeats.

### 🪢 From a second terminal

A named pipe lets one terminal hold the keyboard while another runs the test. This works on macOS and Linux:

```bash
# Terminal 1 — start first; gg waits here until the pipe has a writer
mkfifo /tmp/ggctl
gg --profile load --peak-rps 50 --duration 2m --http-file target.http \
   --headless --reporter json --heartbeat-interval 1s < /tmp/ggctl
```

```bash
# Terminal 2 — keep this shell open; it holds the write end of the pipe
exec 3>/tmp/ggctl
echo '{"id":"b1","command":"bias","amount":15}' >&3
echo '{"id":"s1","command":"stop"}' >&3
```

Replies and heartbeats appear in terminal 1, where `gg` is running. If you close terminal 2's shell before the run ends, `gg` simply stops receiving commands — the run carries on to the end of its plan (see [below](#-limits-and-timing)).

### ⏱ Timed scripts

Piping a file at `gg` is handy, but be aware of how it behaves:

```bash
gg --profile load --headless < commands.jsonl      # every line is sent immediately
```

Every line in the file is consumed as soon as the run starts, so a file of `bias` commands is applied all at once, at t=0 — NDJSON carries no timestamps. To space commands out over the run, use a subshell with `sleep`s (as in the [quick start](#-try-it-in-two-minutes)), or drive them from a small script that writes lines as it goes.

### 🐳 In a container

The published image reads commands from stdin like any other process, but Docker only attaches stdin if you ask:

```bash
(sleep 6; echo '{"id":"b1","command":"bias","amount":25}'; sleep 4; echo '{"id":"s1","command":"stop"}'; sleep 2) \
| docker run --rm -i -v "$PWD":/work -w /work ghcr.io/shyam-s00/gopher-glide:latest \
    --profile load --peak-rps 40 --duration 40s --http-file target.http \
    --headless --reporter json --heartbeat-interval 1s --snap --snap-dir /work/snaps
```

- **`-i` is required.** Without it the container's stdin is closed, `gg` sees end-of-input immediately, every command is silently never delivered, and the run just plays out to the end.
- Don't add `-t` when piping: Docker refuses with `cannot attach stdin to a TTY-enabled container because stdin is not a terminal`. (`-it` is fine if you're typing at an interactive terminal.)
- Reaching a server on your own machine from inside the container? Use `host.docker.internal` instead of `localhost` in the `.http` file (Docker Desktop).
- The container runs as a non-root user, so a snapshot directory must be writable — see [CI & Containers](/ci).

---

## 👀 Reading the output

- **`--reporter json`** is the one to use while driving a run: every heartbeat carries `bias` (the cumulative total), and every command gets exactly one reply (`ack`, `mark` or `error`).
- **`--reporter text`** (the default) prints the replies as readable lines (`bias +20 applied (cumulative +20)`, `mark "cache warmed" (t+12.4s)`), but the periodic heartbeat lines show `target=… actual=…` and **not** the bias value. The `target` number still jumps, so you can see the effect — but use `json` when you want the exact figure.
- In the **TUI**, a `BIAS +N RPS` badge appears in the director bar once the bias is non-zero, and a brief `▲ +5 RPS` / `▼ -5 RPS` flashes when you press a key.

---

## 📏 Limits and timing

- **Bias lands on the next one-second window.** The ack returns immediately and already includes the new cumulative value, but the request rate itself changes at the next window boundary — expect it in the next heartbeat or two. With the default 5 s heartbeat that can look slower than it is; use `--heartbeat-interval 1s` when you're driving a run.
- **End-of-input is not a stop.** If stdin closes — a script finishes, you close the second terminal, a `docker run` without `-i` — the run keeps going until its plan ends. End a scripted run with an explicit `stop`.
- **`stop` is a clean exit**, not an interruption: the snapshot is written, the stream ends with one `stopped` event carrying the final statistics, and the exit code is `0`. (`Ctrl+C` ends a run with `interrupted` instead.)
- **Slider-style controls** should throttle themselves to a few commands a second: every command is applied and nothing is dropped, but the rate only changes once a second.
- **`--control none`** turns the listener off entirely. The run is read-only and `started` reports an empty `capabilities` list.

---

## 🛠 Troubleshooting: "bias isn't working"

| What you see | Likely cause | Fix |
|---|---|---|
| Nothing happens when you type or `echo` in another tab | Commands only reach the terminal that started `gg`. | Type in the run's own terminal, or use the [named pipe](#-from-a-second-terminal). |
| `invalid command line: invalid character …` | The line isn't JSON (`bias 20`). | Send `{"command":"bias","amount":20}`. |
| It seems to work, but you never see a `bias` number | `--reporter text` doesn't print it on heartbeat lines. | Re-run with `--reporter json`, or watch `target=`. |
| No replies at all | Listener off (`--control none`), or stdin isn't attached (`docker run` without `-i`, `< /dev/null`, an IDE output window that doesn't forward keystrokes). | Remove `--control none`; add `-i`; use the IDE's built-in **Terminal** rather than its run/output window. |
| TUI arrow keys do nothing | The terminal running the TUI doesn't have focus, or the run has already finished (keys only act on a running test). | Click into that terminal; start a new run. |
| The rate moved, but later than you expected | Bias applies at the next one-second window; text heartbeats default to every 5 s. | Add `--heartbeat-interval 1s`. |
| Your script ended but the test kept running | End-of-input isn't a stop. | Finish the script with `{"command":"stop"}`. |
| A big `bias` barely changes the rate | The effective rate is `profile target + bias`; a small bias on a large peak is a small change. | Check `target_rps` against the profile's value at that moment. |

Still stuck? Run with `--reporter json` and look for `"event":"error"` lines — every command that couldn't be applied is answered with a `reason` (`parse_error`, `unknown_command`, `invalid_argument`), listed in the [protocol reference](/control-protocol#-replies).
