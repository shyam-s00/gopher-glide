---
title: "IDE Plugins"
---
# IDE Integration

Gopher Glide ships official plugins for both **JetBrains** and **VS Code** (including Open VSX-compatible editors). Both plugins bring the complete `gg` workflow — running traffic simulations, recording and comparing Snapshots, gating CI on regressions — directly into your editor with a native run dashboard and a full Snap Explorer, no terminal required.

---

## JetBrains Plugin

Install from the [JetBrains Marketplace](https://plugins.jetbrains.com/plugin/30983-gopher-glide), or search **Gopher Glide** in **Settings → Plugins → Marketplace**.

Verified on IntelliJ IDEA, GoLand, WebStorm, Rider, PyCharm Community, PhpStorm, and RubyMine. Requires IntelliJ Platform `2024.2+`.

### ⚡ Quick Start

1. Install the plugin — if no `gg` binary is found, the plugin offers to download it for you the moment you open a project.
2. Open any `.http` file and click the **Run GG** gutter icon (or right-click → **Gopher Glide (GG) → Run**).
3. Pick a load profile from the popup — optionally override peak RPS/duration and tick **Record snapshot**.
4. Watch it run live in the **Gopher Glide** tool window at the bottom of the IDE.
5. Switch to the **Snaps** tab to view, diff, assert, or prune recorded snapshots.

### ▶️ Running Traffic Simulations

The plugin adds gutter run icons and a right-click **Gopher Glide (GG)** menu (Project View, Editor, and Editor Tab) to your config and `.http` files. The menu is grouped into **Run**, **Generate**, and **CI** sections.

**On `.http` files:**
- **Run GG** — opens a profile picker covering all **21 built-in load profiles**, grouped by category. Picking a profile opens a dialog pre-filled with that profile's default peak RPS and duration — override as needed — plus a **Record snapshot** checkbox with an optional tag.
- **Run GG (Config)** — only shown when the `.http` file has a sibling `.gg.yaml`; runs that config directly.
- **Generate config.yaml...** — scaffolds a starter `.gg.yaml` for the file without running anything.

**On `.gg.yaml` files:**
- **Run GG** — prompts for optional snapshot recording (with a tag), then runs the config as-is.

### 🖥 Native Run Dashboard

While a simulation runs, the **Run** tab of the **Gopher Glide** tool window updates roughly once per heartbeat (~5s) with:

- Current status and elapsed time
- A row of metric cards: **Target RPS**, **Actual RPS**, **Error Rate**, and **Total Requests**
- A **P50 / P95 / P99** latency card
- A scaled **RPS chart**
- A **stage timeline** — a horizontal progress bar with segment widths proportional to each stage's actual duration
- A **Stop** action to cancel the run cleanly

![Native run dashboard showing live RPS chart, metric cards, and stage timeline](/assets/plugin-run-view.png)

### 🗂 Snap Explorer

The **Snaps** tab is a full native UI for managing Snapshots — list, view, diff, assert, and prune.

Toolbar actions:
- **Refresh** — reload the snapshot list from disk.
- **View Detail** — inspect a snapshot's latency, status distribution, and inferred response schema.
- **Compare (Diff)** — semantically diff two snapshots side-by-side (select exactly two rows).
- **Assert...** — run `gg snap assert` between any two snapshots against configurable thresholds.
- **Prune...** — clean up old snapshots by ID(s), tag, keep-last count, or age (e.g. `30d`) — with a dry-run preview by default.

![Native snapshot diff view highlighting latency, payload, and schema regressions between two snapshots](/assets/plugin-diff-view.png)

### 🛡️ One-Click CI Workflow Generator

**Generate CI Workflow...** (Tools menu, or right-click → **Gopher Glide (GG)**) scaffolds a ready-to-run `.github/workflows/gg.yml` implementing the full headless regression-gating loop with push-to-main baseline capture and PR diff comments.

### ⚙️ Settings & Onboarding

Available under **Settings / Preferences → Tools → Gopher Glide**:
- Set a **custom `gg` binary path** or **custom snapshots directory**.
- **Run panel refresh interval** — controls the `--heartbeat-interval` passed to `gg`.
- **Check for Updates** / **Install Gopher Glide** — one-click binary management with download progress.
- **Copy Diagnostics to Clipboard** — useful when filing a bug report.

### Compatibility

- **IntelliJ Platform baseline:** `2024.2` and newer
- Verified on IntelliJ IDEA, GoLand, WebStorm, Rider, PyCharm Community, PhpStorm, and RubyMine

---

## VS Code Extension

Install from the [VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=gopherglide.gg-plugin) or [Open VSX Registry](https://open-vsx.org/extension/gopherglide/gg-plugin), or search **Gopher-Glide** in the Extensions panel (`Ctrl+Shift+X`).

Compatible with VS Code `1.120.0+` and any Open VSX-compatible editor (VSCodium, Gitpod, etc.).

### ⚡ Quick Start

1. Install the extension — if no `gg` binary is found, it downloads one automatically on first use.
2. Open any `.http` or `.rest` file — a **Run** button appears in the editor title bar and a CodeLens link appears inline above each request.
3. Pick a load profile from the Quick Pick (or let the extension use your `gg.defaultProfile` setting).
4. Watch it run in the **Gopher-Glide** panel at the bottom of the editor.
5. Switch to the **Snaps** tree view to browse, diff, and assert snapshots.

### ▶️ Running Traffic Simulations

Entry points on `.http` / `.rest` files:
- **Editor title bar play button** — one click to run with a profile picker.
- **CodeLens** — inline run/config links above each request block.
- **Right-click context menu** (Editor and Explorer) → **Gopher-Glide** submenu.

On `.gg.yaml` files, the context menu shows **Run (Config)** to run the config directly.

All 21 built-in profiles are available in the Quick Pick, plus optional RPS/duration overrides and snapshot capture.

### 🖥 Native Run Dashboard

The **Run** tab of the **Gopher-Glide** panel shows a live webview updated from `gg`'s headless heartbeat stream:

- Status and elapsed time
- Target/Actual RPS, Error Rate, Total Requests
- P50 / P95 / P99 latency percentiles
- Live RPS chart and stage timeline

### 🗂 Snap Explorer

The **Snaps** tree view in the panel sidebar lets you:
- **View** a snapshot's endpoint breakdown (latency, status distribution, response schema).
- **Diff** any two snapshots — select two entries and click **Diff Snapshots**.
- **Assert** — run `gg snap assert` as a CI-style gate with configurable thresholds.
- **Prune** — clean up old snapshots by ID, tag, or age.

### 🔗 Smart YAML Editing

- **Schema validation & autocomplete** for `.gg.yaml` files — stages, snapshot tuning, and built-in profile names.
- **Go-to-file navigation** — `Ctrl+Click` / `Cmd+Click` a `httpFile:` path to jump to the referenced `.http` file.

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
- Any [Open VSX](https://open-vsx.org/extension/gopherglide/gg-plugin)-compatible editor (VSCodium, Gitpod, Eclipse Theia, etc.)

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
