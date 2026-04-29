# The Terminal UI (TUI)

## TUI Dashboard

```
  gg — Gopher Glide  v0.1.0  ●  RUNNING  ⏱ 00:42

 ╭─ Configuration ──────╮ ╭─ Throughput ─────────╮ ╭─ Latency ────────────╮
 │  Target RPS    50    │ │  RPS         48.2    │ │  Avg      142.3 ms   │
 │  Duration      70s   │ │  Completed   1 446   │ │  Min       88.1 ms   │
 │  Uptime       00:42  │ │  Errors         12   │ │  Max      310.5 ms   │
 │  Active VPUs    18   │ │  Error Rate   0.8%   │ │  P50      138.0 ms   │
 │                      │ │  Jitter       ±10%   │ │  P95      278.4 ms   │
 │                      │ │                      │ │  P99      305.2 ms   │
 ╰──────────────────────╯ ╰──────────────────────╯ ╰──────────────────────╯

 ╭─ Stage Timeline ──────────────────────────────────────────────────────────╮
 │  500 ┤                      ▓▓▓▓▓▓▓▓▓                                    │
 │      │              ░░░░░░░░▓▓▓▓▓▓▓▓▓░░░░░░                              │
 │      │       ░░░░░░░░░░░░░░░                    ░░░░░░░░                  │
 │    0 ┤░░░░░░░                                           ░░░░░░░░░         │
 │      [1/5] Ramp Up  •  stage 0:10 / 0:10  •  total 0:42 / 1:10           │
 ╰───────────────────────────────────────────────────────────────────────────╯

 [↑] +5 rps  [↓] -5 rps  [f] logs (FAILURES ONLY)  [q] quit   BIAS +10 RPS

 ╭─ Call Log ────────────────────────────────────────────────────────────────╮
 │  …scrollable call log…                                                    │
 ╰───────────────────────────────────────────────────────────────────────────╯
```

### Keybindings

| Key            | Action                                            |
|----------------|---------------------------------------------------|
| `↑`            | Increase live RPS by +5 (Director Mode bias)      |
| `↓`            | Decrease live RPS by -5 (Director Mode bias)      |
| `f`            | Toggle call log between all calls and errors only |
| `q` / `Ctrl+C` | Quit                                              |

---

## Director Mode

While a run is in progress, use `↑` / `↓` to apply a live **RPS bias** on top of the configured stage target. The bias accumulates (e.g., three `↑` presses = +15 RPS) and is shown in the TUI and reflected in the live metrics. The bias is applied on top of the LERP'd value — the stage plan continues to run unaffected.
