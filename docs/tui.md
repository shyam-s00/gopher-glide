# The Terminal UI (TUI)

Gopher Glide features a highly polished, interactive Terminal UI designed for real-time observability. Our latest revamp introduces a premium aesthetic powered by a semantic color palette, delivering a clean, modern experience directly in your terminal.

![Gopher Glide TUI Demo](assets/demo.gif)

## TUI Dashboard

The dashboard provides an at-a-glance view of your load test's vital signs, including configuration parameters, real-time throughput metrics, latency percentiles, and a visual stage timeline representing your current progress within the load profile.

### Premium Aesthetics & Themes

The TUI is built on a custom styling engine. It supports full semantic theming so the entire dashboard can be cleanly restyled without touching component code. 

Currently, Gopher Glide defaults to a beautifully balanced **Catppuccin Macchiato** inspired palette, ensuring your load tests look as good as they run.

### Performance & Framerate

The TUI is highly optimized to ensure it doesn't interfere with the load test itself. It targets a smooth **~24 FPS** (refreshing every 42ms) to provide a highly responsive, real-time observability experience without consuming excessive CPU cycles.

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
