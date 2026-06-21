---
title: "JetBrains Plugin"
---
# JetBrains IDE Integration

Gopher Glide features an official [JetBrains plugin](https://plugins.jetbrains.com/plugin/30983-gopher-glide) that brings the **entire `gg` workflow** — running traffic simulations, recording and comparing Snapshots, gating CI on regressions — directly into your IDE. You get a native run dashboard, a full Snap Explorer, and a one-click CI workflow generator, all without ever dropping into a terminal.

---

## ⚡ Quick Start

1. Install the plugin (see [below](#installing-the-plugin)) — if no `gg` binary is found on your machine, the plugin offers to download it for you the moment you open a project.
2. Open any `.http` file and click the **Run GG** gutter icon (or right-click → **Gopher Glide (GG) → Run**).
3. Pick a load profile from the popup — optionally override peak RPS/duration and tick **Record snapshot**.
4. Watch it run live in the **Gopher Glide** tool window at the bottom of the IDE.
5. Switch to the **Snaps** tab to view, diff, assert, or prune recorded snapshots.

The sections below cover every feature in detail.

---

## ▶️ 1. Running Traffic Simulations

The plugin adds gutter run icons and a right-click **Gopher Glide (GG)** menu (Project View, Editor, and Editor Tab) to your config and `.http` files. The menu is grouped into **Run**, **Generate**, and **CI** sections.

**On `.http` files:**
- **Run GG** — opens a profile picker covering all **21 of `gg`'s built-in load profiles**, grouped by category (E-Commerce & High-Demand Events, Standard Testing & CI/CD, Resilience & Chaos, Auto-Scaling & Infrastructure, Specialized Traffic). Picking a profile opens a dialog pre-filled with that profile's own default peak RPS and duration — leave a field untouched to use the profile's default, or override it — plus a **Record snapshot** checkbox with an optional tag.
- **Run GG (Config)** — only shown when the `.http` file has a sibling `.gg.yaml`; runs that config directly with no profile or overrides (just the snapshot checkbox/tag), since the config is the single source of truth for everything else.
- **Generate config.yaml...** — scaffolds a starter `.gg.yaml` for the file without running anything.

**On `.gg.yaml` files:**
- **Run GG** — prompts for optional snapshot recording (with a tag), then runs the config as-is.

Every run executes through the plugin's native run dashboard (below) rather than launching `gg`'s interactive terminal UI — this isn't just a style choice, it sidesteps a CPU-pinning redraw-rate issue in the terminal TUI that can otherwise freeze or crash the IDE process hosting it.

---

## 🖥 2. The Native Run Dashboard

While a simulation runs, the **Run** tab of the **Gopher Glide** tool window updates roughly once per heartbeat (~5s by default) with:

- Current status and elapsed time
- A row of metric cards: **Target RPS**, **Actual RPS**, **Error Rate**, and **Total Requests**
- A **P50 / P95 / P99** latency card
- A scaled **RPS chart**
- A **stage timeline** — a horizontal progress bar across the config's `stages`, with segment widths proportional to each stage's actual duration, highlighting the stage currently running
- A **Stop** action to cancel the run cleanly

![Native run dashboard showing live RPS chart, metric cards, and stage timeline](/assets/plugin-run-view.png)

---

## 🗂 3. The Snap Explorer

The **Snaps** tab of the same tool window is a full native UI for managing Snapshots — list, view, diff, assert, and prune, no `gg snap view`/`diff` TUI required.

| Column | Description |
|---|---|
| ID / Tag | Numeric ID plus the user-supplied tag (or "(untagged)") |
| Date | When the test run began |
| Total Requests | Aggregate request count |
| Peak RPS | Peak requests-per-second recorded |

Toolbar actions:

- **Refresh** — reload the snapshot list from disk.
- **View Detail** — inspect a snapshot's latency, status distribution, and inferred response schema (select one row, or double-click).
- **Compare (Diff)** — semantically diff two snapshots side-by-side (select exactly two rows) — the same metrics shift, payload shift, and schema drift analysis as `gg snap diff`, rendered natively.
- **Assert...** — run `gg snap assert` between any two snapshots (by row selection) against configurable thresholds: P99 latency regression % (default 20%), error rate increase in percentage points (default 5), average payload size increase % (default 50, warning-level), plus checkboxes to treat removed schema fields as a regression and to fail on warnings too. Results show a pass/fail breakdown per endpoint.
- **Prune...** — clean up old snapshots by ID(s), tag, keep-last count, or age (e.g. `30d`) — with a dry-run preview checked by default, so nothing is deleted until you explicitly uncheck it, mirroring `gg snap prune`'s safety model.

![Native snapshot diff view highlighting latency, payload, and schema regressions between two snapshots](/assets/plugin-diff-view.png)

---

## 🛡️ 4. One-Click CI Workflow Generator

The **Generate CI Workflow...** action (Tools menu, or right-click → **Gopher Glide (GG)**) scaffolds a ready-to-run `.github/workflows/gg.yml` implementing the full headless regression-gating loop:

- **On push to `main`:** run a headless simulation, snapshot it as the baseline (`--snap-tag main`), prune to the latest one, and cache it for later runs.
- **On every pull request:** restore the latest cached baseline, run and snapshot the PR build, run `gg snap assert` against the baseline, and post (or update) a sticky PR comment with the Markdown report — failing the job if a regression is detected.

The generated workflow pre-fills the config path from the first `.gg.yaml` found anywhere in your project (skipping `.git`, `.idea`, `.gradle`, `build`, `out`, and `node_modules`), falling back to a placeholder if none exists. You're prompted before it overwrites an existing `gg.yml`. The first PR before any baseline exists is handled gracefully (it posts a "no baseline yet" comment instead of failing).

---

## 📄 5. Scaffolding a New Test

**File → New → Add GG http file** creates a `.http` file pre-filled with a sample request and a cheat sheet of every built-in `gg` profile in a comment header — a fast starting point for a brand-new endpoint.

---

## 🔗 6. Smart YAML Editing

- **Auto-complete & validation** — `.gg.yaml` files are validated against the bundled Gopher-Glide JSON Schema, including `stages[].name` and the `snap:` tuning block, with inline errors and completion for every config key.
- **Clickable file references** — `Ctrl+Click` / `Cmd+Click` any file path inside a `.gg.yaml` config to jump straight to the referenced file.

---

## ⚙️ 7. Settings & First-Run Onboarding

Available under **Settings / Preferences → Tools → Gopher Glide**:

- Set a **custom `gg` binary path**, or a **custom snapshots directory**.
- **Run panel refresh interval** — a 0–60s spinner controlling how often `gg` emits heartbeat updates to the run dashboard (`--heartbeat-interval`). `0` defers to `gg`'s own 5s default. Takes effect on the next run, since `gg` reads this at process startup rather than live.
- If no `gg` binary is found, the plugin **proactively offers to download** the latest release the moment you open a project — with visible progress — instead of silently hanging on your first Run click.
- **Check for Updates** (shown as **Install Gopher Glide** when nothing's installed yet) compares your local version against the latest release and updates with one click. The panel also shows whether the binary in use is plugin-managed or resolved from your system `PATH`, and its exact path.
- **Copy Diagnostics to Clipboard** collects OS, binary paths/permissions, and version info — useful when filing a bug report.

---

## Compatibility

- **IntelliJ Platform baseline:** `2024.2` and newer
- Verified on IntelliJ IDEA, GoLand, WebStorm, Rider, PyCharm Community, PhpStorm, and RubyMine
- Any IDE built on IntelliJ Platform `2024.2+` with the bundled `JSON` and `YAML` plugins should work

---

## 🔭 Looking Ahead: VS Code Support

A VS Code extension is in **early, exploratory development**, aiming to bring the same run dashboard and Snap Explorer to VS Code's UI. It's pre-alpha — not yet released, not yet feature-complete, and without a committed release date — so treat it as a direction being explored rather than something to plan around just yet. Check back here soon for updates.

---

## Installing the Plugin

<a href="https://plugins.jetbrains.com/plugin/30983-gopher-glide" class="md-button md-button--primary">
  Download from JetBrains Marketplace
</a>

Or install manually:

1. Open your JetBrains IDE (IntelliJ IDEA, GoLand, WebStorm, etc.).
2. Go to **Settings > Plugins > Marketplace**.
3. Search for **Gopher Glide**.
4. Click **Install**.
