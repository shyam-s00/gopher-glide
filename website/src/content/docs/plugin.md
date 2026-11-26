---
title: "IDE Plugins"
---

Gopher Glide ships official plugins for both **JetBrains** and **VS Code** (including Open VSX-compatible editors). Both plugins bring the complete `gg` workflow (running traffic simulations, recording and comparing Snapshots, gating CI on regressions) directly into your editor with a native run dashboard and a full Snap Explorer, no terminal required.

---

## JetBrains Plugin

Install from the [JetBrains Marketplace](https://plugins.jetbrains.com/plugin/30983-gopher-glide), or search **Gopher Glide** in **Settings → Plugins → Marketplace**.

Verified on IntelliJ IDEA, GoLand, WebStorm, Rider, PyCharm Community, PhpStorm, and RubyMine. Requires IntelliJ Platform `2024.2+`.

### ⚡ Quick Start

1. Install the plugin: if no `gg` binary is found, the plugin offers to download it for you the moment you open a project.
2. Open any `.http` file and click the **Run GG** gutter icon (or right-click → **Gopher Glide (GG) → Run**).
3. Pick a load profile from the popup: optionally override peak RPS/duration and tick **Record snapshot**.
4. Watch it run live in the **Gopher Glide** tool window at the bottom of the IDE.
5. Switch to the **Snaps** tab to view, diff, assert, or prune recorded snapshots.

### ▶️ Running Traffic Simulations

The plugin adds gutter run icons and a right-click **Gopher Glide (GG)** menu (Project View, Editor, and Editor Tab) to your config and `.http` files. The menu is grouped into **Run**, **Generate**, and **CI** sections.

**On `.http` files:**
- **Run GG**: opens a profile picker covering all **21 built-in load profiles**, grouped by category. Picking a profile opens a dialog pre-filled with that profile's default peak RPS and duration (override as needed) plus a **Record snapshot** checkbox with an optional tag.
- **Run GG (Config)**: only shown when the `.http` file has a sibling `.gg.yaml`; runs that config directly.
- **⚡ Autopilot: Find Ceiling…** (`gg` v1.5.0+): at the top of the picker. `gg` steps the rate up while health holds, marks where it breaches, confirms the rate it backs off to, and reports the highest sustainable RPS. No LLM involved.
- **Generate config.yaml...**: scaffolds a starter `.gg.yaml` for the file without running anything.

**On `.gg.yaml` files:**
- **Run GG**: prompts for optional snapshot recording (with a tag), then runs the config as-is.

<img src="/assets/plugin-profile-picker.png" alt="The Run with Profile popup: Autopilot Find Ceiling at the top, then all built-in profiles grouped by category" style="max-width: 260px; height: auto; display: block;" loading="lazy" />

### 🖥 Native Run Dashboard

While a simulation runs, the **Run** tab of the **Gopher Glide** tool window updates roughly once per heartbeat (~5s) with:

- Current status and elapsed time
- A row of metric cards: **Target RPS**, **Actual RPS**, **Error Rate**, and **Total Requests**
- A **P50 / P95 / P99** latency card
- A scaled **RPS chart**
- A **stage timeline**: a horizontal progress bar with segment widths proportional to each stage's actual duration
- A **Stop** action that ends the run cleanly: `gg` finishes writing the snapshot first. If it doesn't answer, the plugin interrupts it, and a second click forces it to end.

**Live control** (JetBrains plugin 7.0+, `gg` v1.3.0+) adds a strip above the metric cards while a run is in progress:
- **Bias**: **−10 / −5 / +5 / +10** change the target RPS without stopping the run; **Reset** returns to the planned rate. The badge shows the bias `gg` reports.
- **Mark…**: drop a named marker at the current point of the run. With `gg` v1.5.0+ a mark is stamped when you click **Mark…**, not when you confirm its label; with older versions it is stamped when you confirm.
- Bias changes and marks are drawn on the RPS chart, and both are recorded in the snapshot.

With `gg` v1.5.0+ the strip also has:
- **Hold RPS**: pin the rate at a number you choose, ignoring the profile's ramp, until you click **Release Hold**. A **📌 Hold N RPS** badge shows while it is in force.
- **🛡️ Guard SLO…**: start the built-in SLO guard mid-run with your own p99 and error-rate limits. It clamps the rate when health breaches and releases it in steps. While an autopilot runs, bias and hold are off and **Stop Autopilot (Take Over)** hands the rate back. You can also protect a whole run from the run dialogs.
- Marks the autopilot drops itself appear on the chart with a ⚡ prefix; yours carry 👤, and an AI agent's carry 🤖.

**Failure breakdown** (`gg` v1.5.0+): each heartbeat can carry an `error_mix` object saying what kind of failures are happening right now: connection errors, 4xx, 5xx, extraction failures and the top status codes. The JetBrains plugin shows it as a one-line **Recent errors** strip under the metric cards, and in the error-rate and latency card tooltips. See the [Live Control Protocol](/control-protocol#-heartbeat-additions).

With an older `gg`, the strip is replaced by a short notice that names the installed version and offers to install or upgrade it. The run itself works as before.

![Native run dashboard after an autopilot ceiling search: metric cards, Live Control with a hold badge, and the RPS chart with the autopilot's marks](/assets/plugin-run-dashboard.png)

### 🗂 Snap Explorer

The **Snaps** tab is a full native UI for managing Snapshots: list, view, diff, assert, and prune.

Toolbar actions:
- **Refresh**: reload the snapshot list from disk.
- **View Detail**: inspect a snapshot. The detailed view provides an **Endpoint Breakdown** (Error Rate, P95, Payload Avg) and a dedicated **Inferred JSON Schema** viewer showing the precise `Type`, `Presence %`, and `Stability` of every field observed during the run. For runs recorded with `gg` v1.3.0+, a **Manual changes** section lists every mark and bias change in time order, with final bias, peak bias, how long a bias was in effect, and when the first change happened.
- **Compare (Diff)**: semantically diff two snapshots side-by-side (select exactly two rows). The diff UI automatically flags **Configuration mismatches** (e.g., if you diff a baseline run with a different config hash) and highlights specific schema regressions, such as a field's presence dropping from 100% to 19% under heavy load. When either run had marks or bias changes made by hand, a one-line notice says so, since those runs may not be like for like.
- **Assert...**: run `gg snap assert` between any two snapshots against configurable thresholds.
- **Export Mission Report…** and **Copy PR Markdown** (`gg` v1.5.0+): save a snapshot's report as Markdown or HTML, or put it on the clipboard as Markdown for a pull request. In the Snapshot View, the export button is a split button with **Markdown (.md)** and **HTML (.html)**, and it remembers your last choice.
- **Prune...**: clean up old snapshots by ID(s), tag, keep-last count, or age (e.g. `30d`): with a dry-run preview by default.

![Native snapshot detail view showing inferred JSON schema stability and presence](/assets/plugin-snap-view.png)

![Native snapshot diff view highlighting configuration warnings and schema regressions between two snapshots](/assets/plugin-diff-view.png)

A run steered by the autopilot shows an outcome banner above the list of manual changes, and the Snapshot View lists who made each change.

![Snapshot View for an autopilot run: outcome banner, manual changes table, and the Export as Markdown or HTML menu](/assets/plugin-snap-export.png)

### 🤖 AI Agents

Needs `gg` v1.5.0+. **Tools → Connect AI Agents to Gopher Glide…** (or the **AI Agents** button in the Run tab) connects Claude Code, Cursor, Junie, JetBrains AI Assistant and JetBrains Air to `gg`, so a run an agent starts shows up live in the Run tab like one of yours.

- **One dialog, nothing silent.** It shows what is linked now, previews every change, and writes only what you tick. Your other servers and agents are kept, and an entry of yours is never overwritten unless you choose **Replace**.
- **Workspace `.mcp.json`** gets a `gg` server that attaches to the IDE's shared daemon (`gg mcp --attach`), so agent runs are visible and steerable in the IDE. Claude Code, Cursor and Junie use their own model to decide what to run.
- **JetBrains Air** gets the `gg autopilot` agent in `~/.jetbrains/acp.json`. Its slash commands need no API key or model. Free-text requests need a Gemini key or an OpenAI-compatible server, set in an optional section of the dialog.
- **In the Run tab**, an agent's run opens with a **STARTED BY AI AGENT** badge, and its marks, bias steps and holds appear on the chart with a 🤖 prefix. If you are mid-run, it waits and takes the tab when yours ends.
- **Your runs stay yours.** Agents can read a run you started but cannot steer it until you tick **🤖 Allow AI Steer**. It is off for every new run, and it appears on runs you start through the shared daemon (**Settings → Gopher Glide → Run my simulations through the shared daemon**).
- **Limits for agents** (max RPS, max duration, runs at once, and which hosts they may target) are in the dialog. By default agents may only target the hosts in your project's `.http` files.

![The Connect AI Agents dialog: shared daemon, mcp.json, agent skill and JetBrains Air status, with options to set each one up](/assets/plugin-connect-agents.png)

![A JetBrains Air agent session beside the Run tab, which shows the agent's run with a Started by AI Agent badge](/assets/plugin-agent-run.png)

### 🛡️ One-Click CI Workflow Generator

**Generate CI Workflow...** (Tools menu, or right-click → **Gopher Glide (GG)**) scaffolds a ready-to-run `.github/workflows/gg.yml` implementing the full headless regression-gating loop with push-to-main baseline capture and PR diff comments. See [CI & Containers](/ci) for what that workflow does under the hood, and for the container image if you're wiring up a different CI system by hand.

### ⚙️ Settings & Onboarding

Available under **Settings / Preferences → Tools → Gopher Glide**:
- Set a **custom `gg` binary path** or **custom snapshots directory**.
- **Run panel refresh interval**: controls the `--heartbeat-interval` passed to `gg`.
- **Check for Updates** / **Install Gopher Glide**: one-click binary management with download progress.
- **Check for gg updates on startup**: on by default; looks for a newer release at most once a day and shows one notification with **Update Now**, **Open Settings** and **Skip This Version**. It stays quiet when you're offline, already current, or use a custom binary path.
- **Copy Diagnostics to Clipboard**: useful when filing a bug report.

### Compatibility

- **IntelliJ Platform baseline:** `2024.2` and newer
- Checked with the JetBrains Plugin Verifier against IntelliJ IDEA from `2024.2` to `2026.3`
- Verified on IntelliJ IDEA, GoLand, WebStorm, Rider, PyCharm Community, PhpStorm, and RubyMine

---

## VS Code Extension

Install from the [VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=gopherglide.gg-plugin) or [Open VSX Registry](https://open-vsx.org/extension/gopherglide/gg-plugin), or search **Gopher Glide** in the Extensions panel (`Ctrl+Shift+X`).

Compatible with VS Code `1.120.0+` and any Open VSX-compatible editor (VSCodium, Gitpod, etc.).

### ⚡ Quick Start

1. Install the extension: if no `gg` binary is found, it downloads one automatically on first use.
2. Open any `.http` or `.rest` file: a **Run** button appears in the editor title bar and a CodeLens link appears inline above each request.
3. Pick a load profile from the Quick Pick (or let the extension use your `gg.defaultProfile` setting).
4. Watch it run in the **Gopher Glide** panel at the bottom of the editor: and steer it live from there (see [Live control](#live-control-in-vs-code)).
5. Switch to the **Snaps** tree view to browse, diff, and assert snapshots.

### ▶️ Running Traffic Simulations

Entry points on `.http` / `.rest` files:
- **Editor title bar play button**: one click to run with a profile picker.
- **CodeLens**: inline run/config links above each request block.
- **Right-click context menu** (Editor and Explorer) → **Gopher Glide** submenu.

On `.gg.yaml` files, the context menu shows **Run (Config)** to run the config directly.

All 21 built-in profiles are available in the Quick Pick, plus optional RPS/duration overrides and snapshot capture.

### 🖥 Native Run Dashboard

The **Gopher Glide** panel at the bottom of the editor shows a live webview updated from `gg`'s headless heartbeat stream:

- Status and elapsed time
- Target/Actual RPS, Error Rate, Total Requests
- P50 / P95 / P99 latency percentiles
- Live RPS chart and stage timeline
- A **Stop** button that ends the run cleanly: `gg` finishes writing the snapshot first. If `gg` is stuck, the button becomes **Force Stop**.

![Gopher Glide run dashboard in VS Code: live RPS chart with bias ticks and mark lines, the Director Bias bar and metric cards](/assets/vscode-run-view.png)

#### Live control in VS Code

*(VS Code extension 3.0+, `gg` v1.3.0+.)* A control bar sits above the chart while a run is in progress:

- **Director Bias**: **−10 / −5 / +5 / +10** change the target RPS without stopping the run; **Reset** returns to the planned rate. The badge shows the total bias in effect. `gg` applies a change within about a second, and the chart follows at the next heartbeat (set `gg.heartbeatIntervalSeconds` lower for snappier feedback).
- **Drop Mark**: asks for a label (for example `cache-cleared`) and drops a named marker at the current point of the run. A mark is stamped when `gg` receives it, which is when you confirm the label.
- Bias changes and marks are drawn on the RPS chart (ticks on the target line, labeled dashed lines for marks) and both are recorded in the snapshot.
- **Stop** asks `gg` to finish the run itself, so a `--snap` snapshot is written with every bias change and mark. While `gg` wraps up the button reads *Stopping…* and then turns into **Force Stop**; a double-click can't accidentally cut off a snapshot being written.

With an older `gg`, the controls are replaced by a short notice naming the installed version, with the next step that fits how `gg` was installed: **Update gg** for the copy the extension manages, **Install managed gg** if yours comes from `$PATH`, or **Open Settings** if you picked a custom binary or use `pathOnly` mode. The run itself works as before.

### 🗂 Snap Explorer

The **Snaps** tree view in the panel sidebar lets you:
- **View** a snapshot's endpoint breakdown (latency, status distribution, response schema). For runs recorded with `gg` v1.3.0+, a **Manual changes** section lists every mark and bias change in time order, with final bias, peak bias, how long a bias was in effect, and when the first change happened. Snapshots from older versions say that marks and bias changes were not recorded.
- **Diff** any two snapshots: select two entries and click **Diff Snapshots**. When either run had marks or bias changes made by hand, a one-line notice says so, since those runs may not be like for like.
- **Assert**: run `gg snap assert` as a CI-style gate with configurable thresholds.
- **Prune**: clean up old snapshots by ID, tag, or age.

![Snapshot view in VS Code with the Manual changes list, bias figures and inferred JSON schema](/assets/vscode-snap-view.png)

![Snapshot diff in VS Code with the manual-changes notice, per-endpoint deltas and status-code distribution](/assets/vscode-snap-diff.png)

### 🔗 Smart YAML Editing

- **Schema validation & autocomplete** for `.gg.yaml` files: stages, snapshot tuning, and built-in profile names.
- **Go-to-file navigation**: `Ctrl+Click` / `Cmd+Click` a `httpFile:` path to jump to the referenced `.http` file.

### ⚙️ Extension Settings

| Setting | Default | Description |
|---|---|---|
| `gg.binaryPath` | `""` | Absolute path to the `gg` binary. Leave empty to use the auto-managed binary. |
| `gg.autoUpdateCheck` | `true` | Check for a newer `gg` CLI version on activation. |
| `gg.installationMode` | `"auto"` | `auto` downloads automatically, `manual` prompts first, `pathOnly` never downloads. |
| `gg.defaultProfile` | `""` | Profile to pre-select in the Quick Pick (e.g. `flash-sale`). |
| `gg.snapshotsDir` | `""` | Override directory for snapshot storage (`--snap-dir`). |
| `gg.heartbeatIntervalSeconds` | `0` | Heartbeat cadence for headless runs. `0` uses `gg`'s own default (5s). |

### Compatibility

- **VS Code** `1.120.0+`
- **Live control** needs `gg` v1.3.0+ (the extension's auto-managed binary stays current, so most installs get it automatically); everything else works with older versions
- Any [Open VSX](https://open-vsx.org/extension/gopherglide/gg-plugin)-compatible editor (VSCodium, Gitpod, Eclipse Theia, etc.)

---

## 🎛 Live Control Protocol

Both plugins already talk to `gg` over its headless heartbeat stream. As of v1.3, that stream is two-way: `gg --headless` accepts commands over stdin (`bias`, `mark`, `stop`) and replies inline, without killing the process. See [Driving a Run Live](/live-control) to try it by hand and the [Live Control Protocol](/control-protocol) for the full spec. Both the JetBrains plugin (7.0+) and the VS Code extension (3.0+) are built on it: the run dashboard drives `bias`, `mark` and `stop` live, and a clean Stop keeps the snapshot.

**Coming next: agents and the IDE share one run.** gg v1.5 adds a daemon mode (`gg mcp --port`) that streams every run, including ones an AI agent starts, to a plugin over server-sent events, with Stop and bias controls staying in the plugin. The plugin releases that use it follow gg v1.5. The protocol is documented now for tool builders: [The IDE bridge](/control-protocol#the-ide-bridge). Until then, [`gg mcp init`](/mcp#one-command) and [`gg autopilot`](/autopilot) work alongside the plugins.

**Marks with a confirm step:** a `mark` can carry the offset captured when the user clicked (`elapsed_s`, `gg` v1.5.0+), so a slow label dialog doesn't shift the mark later in the run. Send it only when the `started` event's `capabilities` contains `control.mark.elapsed`; otherwise send the mark without it. Details in [For plugin authors: marks with a confirm step](/control-protocol#-for-plugin-authors-marks-with-a-confirm-step).

**A note for anyone building their own tooling on the protocol:** it carries a generic signed `amount` for bias, not a fixed ±5 step (the widget is a UI decision, not a protocol one. Both plugins use a **stepper (± buttons)** for parity with the TUI's `↑`/`↓` feel; a slider is a reasonable alternative using the exact same command, just **debounce drag events** (throttle to a few per second)) the request rate only changes once a second, so one command per pixel of drag adds noise without adding precision.

---

## Installing the Plugins

<div style="display: flex; gap: 1rem; flex-wrap: wrap; margin-bottom: 1.5rem;">

<a href="https://plugins.jetbrains.com/plugin/30983-gopher-glide" class="md-button md-button--primary">
  JetBrains Marketplace
</a>

<a href="https://marketplace.visualstudio.com/items?itemName=gopherglide.gg-plugin" class="md-button md-button--primary">
  VS Code Marketplace
</a>

<a href="https://open-vsx.org/extension/gopherglide/gg-plugin" class="md-button">
  Open VSX Registry
</a>

</div>
