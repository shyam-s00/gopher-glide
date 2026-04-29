# Gopher Glide Snapshots (`gg snap`)

Typical load testing tools report basic metrics at the end of a run (e.g., P99 latency, RPS) and then discard the underlying data. **Gopher Glide (gg)** takes a radically different approach with its **Snap** feature.

Instead of just outputting numbers, `gg` captures a replayable **Behavioral Profiling Snapshot** of your API under load. By continuously sampling your API's responses during a load phase, `gg` records latency, status distributions, and intelligently infers the actual *semantic shape* (JSON schema) of the payloads returned. 

This enables **Semantic Diffing** and automated **Time-Travel Regression Triage**—allowing you to explicitly measure not just *when* your API slows down, but *how* its behavior and response shape dynamically change under pressure.

---

## The Power of Snapshots

With `gg snap`, you can:
- 🕰 **Time-Travel:** Review the exact behavioral profile (payload schemas, latency distros, error rates) of a run performed days or weeks ago.
- 🔬 **Semantic Diffing:** Compare two runs and see exactly what changed. Did P99 latency jump 38% because a *new* database field was suddenly injected into the JSON payload? `gg` will tell you.
- 🚦 **CI Regression Gates:** Use `gg snap assert` in your CI pipelines to enforce hard limits on performance and payload regressions. Break the build if an endpoint degrades.

---

## 📸 1. Capturing a Snapshot

To take a snapshot, simply add the `--snap` flag to any load test. 

```bash
gg config.yaml --snap
```

You can optionally **tag** your snapshot to easily identify it later (e.g., baseline vs. PR branch):

```bash
gg config.yaml --snap --snap-tag "v1-baseline"
```

A lightweight JSON `.snap` file is written to your system's configuration directory (e.g., `~/.config/gg/snapshots/` on Linux/macOS). An indicator (`📸 Snapping`) will appear in your TUI during the run.

### Configuring Schema Inference Engine Limits
`gg` smartly samples a fraction of the request bodies to infer JSON schemas without tanking the load generator's performance. By default, it samples 5% of bodies.

You can configure these limits in your `config.yaml` to strike the right balance between performance and thoroughness:
```yaml
snap:
  sample_rate: 0.05       # Analyze 5% of responses for schema building
  max_samples: 200        # Store up to 200 JSON body samples per-endpoint
  max_body_kb: 500        # Byte limit (500 KB) for total samples stored per-endpoint
```
*(You can also override these via CLI flags: `--snap-sample`, `--snap-max-samples`, `--snap-max-body-kb`)*.

---

## 🗂 2. Managing Snapshots

Managing your snapshot history is built right into the CLI. You don't need a config file to use these management commands.

### Listing Snapshots
To see all saved snapshots on your machine:
```bash
gg snap list
```
Returns a neat table:
```
ID   TAG           DATE                 REQUESTS   PEAK RPS   ENDPOINTS
--   ---           ----                 --------   --------   ---------
2    v1-branch     2026-04-20 14:45     45,000     500        3
1    v1-baseline   2026-04-20 14:30     45,000     500        3
```

### Viewing a Snapshot
Explore the detailed profile (latency per endpoint, status code distribution, and the inferred JSON schemas) using a rich Terminal UI:

```bash
gg snap view v1-baseline 
# Or target by ID: gg snap view 1
```

---

## 🔍 3. Semantic Diffing (`gg snap diff`)

This is where `gg` stands apart from other tools. You can visually diff two runs side-by-side to understand *why* performance changed.

```bash
gg snap diff v1-baseline v1-branch
# Or by ID: gg snap diff 1 2
```

The diff engine compares the Baseline (first argument) against the Current (second argument) and highlights:
- **Metrics Shift:** Changes in Latency (Avg, P95, P99, Max) and Error Rates.
- **Payload Shift:** Average and max payload size growth (e.g., "Payload size grew by 1.2MB").
- **Schema Drift:** Specifically calls out JSON Schema changes. `+` for newly added fields, `-` for removed fields, and `~` for fields that changed type (e.g., `string` to `integer`).

Green, yellow, and red borders make it easy to instantly see which endpoints **Passed**, received a **Warning**, or suffered a **Regression**.

---

## 🛡️ 4. CI Integration & Automated Assertions

You can leverage snapshots directly in your Continuous Integration (CI) pipelines to stop performance regressions before they merge. 

### The `assert` Command
Use `gg snap assert` to compare a new build against a baseline. If the new build breaches your thresholds, `gg` automatically fails the pipeline (`exit code 1`).

```bash
gg snap assert --baseline main --current pr-123 \
    --latency-regression 10 \
    --error-rate-delta 0.05 \
    --payload-size-delta 50 \
    --deny-removed-fields
```

**Threshold Flags Breakdown:**
- `--latency-regression 10`: Triggers a failure if P99 latency increases by more than 10%.
- `--error-rate-delta 0.05`: Triggers a failure if the error rate increases by an absolute 5 percentage points.
- `--payload-size-delta 50`: Triggers a warning if payload sizes increase by 50%.
- `--deny-removed-fields`: Ensures backward compatibility. If any JSON fields are removed from the payload output, it fails the build.
- `--fail-on-warn`: Upgrades all warnings (like payload growth) into hard pipeline failures.

### Headless execution (`--headless`)
In CI/CD environments, you don't want an interactive terminal UI. `gg` provides a fully decoupled **Headless Mode**.

Instead of a dashboard, `gg` will stream structured heartbeat logs to `stdout`.

```bash
# Capture the snapshot in headless mode
gg config.yaml --snap --snap-tag "pr-123" --headless

# Make your assertions, outputting the result in Markdown format for a PR Comment
gg snap assert --baseline main --current pr-123 \
    --latency-regression 10 \
    --reporter md > report.md
```

The `--reporter` flag supports `text`, `json`, and `md`. When generating Markdown reports, `gg` produces beautiful, actionable summaries perfect for injecting directly as a GitHub or GitLab Pull Request comment!

---

## Where are Snapshots saved?

`gg` uses the idiomatic, cross-platform configuration directory for your OS:
- **macOS**: `~/Library/Application Support/gg/snapshots/`
- **Linux**: `~/.config/gg/snapshots/`
- **Windows**: `%APPDATA%\gg\snapshots\`

You can manually override this path on any command using the `--snap-dir <path>` flag.

---

## 🧹 5. Pruning Snapshots (`gg snap prune`)

As you run more load tests, your snapshot directory can grow. You can automatically clean up old snapshots using `gg snap prune`.

```bash
# Keep the 10 most recent snapshots, delete the rest
gg snap prune --keep-last 10

# Delete anything older than 30 days
gg snap prune --older-than 30d

# Delete all snapshots with a specific tag
gg snap prune --tag "pr-123"
```

**Safety First**: By default, `gg` will display a confirmation prompt listing all the snapshots it intends to delete before proceeding.
- Use `--dry-run` to see what would be deleted without actually doing it.
- Use `--yes` to skip the prompt (useful for automated cleanup in CI/CD).
