---
title: "CI & Containers"
description: "Run gg in GitHub Actions (or any CI) using the published container image — headless load tests and snapshot regression gates."
---

`gg` ships a container image alongside every release, and a `--headless` mode built specifically for pipelines. This page covers running the image directly, and wiring both a plain load test and a `gg snap assert` regression gate into GitHub Actions.

> **Using the JetBrains plugin?** You don't have to hand-write any of the YAML below — **Generate CI Workflow...** (Tools menu, or right-click → **Gopher Glide (GG)**) scaffolds a ready-to-run `.github/workflows/gg.yml` implementing the same push-to-main baseline capture + PR diff comment loop shown here. See the [One-Click CI Workflow Generator](/plugin) section of the JetBrains plugin docs. The walkthrough below is for everyone else — other editors, or GitLab/CircleCI/Jenkins pipelines you're wiring up by hand.

---

## The container image

Every tagged release publishes a `linux/amd64` image to GitHub Container Registry:

```bash
docker pull ghcr.io/shyam-s00/gopher-glide:latest
docker pull ghcr.io/shyam-s00/gopher-glide:1.0.0   # pin to a specific version
```

It's built on `distroless/static-debian12:nonroot` — no shell, no package manager, just the `gg` binary as `ENTRYPOINT`. It's a standard OCI image, so `podman` (a drop-in CLI replacement for `docker`) or any other OCI-compliant runtime works the same way — swap `docker` for `podman` in every command on this page and nothing else changes. Anything you'd normally pass to `gg` on the command line goes after the image name:

```bash
docker run --rm \
  -v "$PWD":/work -w /work \
  ghcr.io/shyam-s00/gopher-glide:latest \
  config.yaml --headless --yes
```

Two things fall out of "distroless + nonroot" that matter for CI:

- **No shell.** You can't `docker exec` into it or chain commands with `&&` inside the container — pass flags directly as the `docker run` command.
- **Runs as UID `65532`, not root.** If `gg` needs to write anything back to a mounted volume (snapshot files, `--out` reports), that directory must be writable by that UID. On most CI runners the checked-out workspace is owned by the runner's own user, so `gg` running as `65532` won't have write access to it by default. The simplest fix is a world-writable output directory and an explicit `--snap-dir`, rather than relying on the container's default config dir:

  ```bash
  mkdir -p .gg-snapshots && chmod 777 .gg-snapshots

  docker run --rm \
    -v "$PWD":/work -w /work \
    ghcr.io/shyam-s00/gopher-glide:latest \
    config.yaml --headless --yes --snap --snap-dir /work/.gg-snapshots
  ```

Prefer a native binary instead? `actions/setup-go`-style tooling doesn't apply here since `gg` isn't a Go library step — grab a prebuilt binary from [GitHub Releases](https://github.com/shyam-s00/gopher-glide/releases/latest) or `brew install shyam-s00/tap/gg` on a macOS/Linux runner and skip containers entirely. The container image mainly earns its keep when your pipeline is already container-based (a Docker-in-Docker step, a Kubernetes Job, GitLab's Docker executor, etc.) or you want a pinned, reproducible `gg` version without touching the runner's toolchain.

---

## GitHub Actions: a headless load test

A minimal job that runs a load test against a service already up in the same job (a service container, a docker-compose stack, or a URL passed via `--http-file`) and fails the build on any request errors:

```yaml
name: Load Test

on:
  pull_request:
    branches: ["main"]

jobs:
  load-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6

      # ... start your service under test here ...

      - name: Run gg
        run: |
          docker run --rm \
            -v "${{ github.workspace }}":/work -w /work \
            ghcr.io/shyam-s00/gopher-glide:latest \
            config.yaml --profile smoke --headless --yes --reporter json
```

`--headless` swaps the interactive TUI for line-delimited heartbeat logs, and `--yes` skips any confirmation prompt — both are required for non-interactive use. See [Headless mode & TUI output](/cli-reference#headless-mode--tui-output) for the full flag list and the JSON payload shape.

---

## GitHub Actions: a snapshot regression gate

The more interesting pipeline use case is [`gg snap assert`](/snap#the-assert-command) — comparing a PR's behavioral snapshot against a baseline and failing the build on latency, error-rate, or schema regressions. The baseline snapshot needs to survive between workflow runs, which `actions/cache` handles well:

```yaml
name: API Regression Gate

on:
  push:
    branches: ["main"]
  pull_request:
    branches: ["main"]

jobs:
  gg:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6

      # ... start your service under test here ...

      - name: Restore baseline snapshot
        uses: actions/cache@v4
        with:
          path: .gg-snapshots
          key: gg-snapshots-main

      - name: Prep writable snapshot dir
        run: mkdir -p .gg-snapshots && chmod 777 .gg-snapshots

      - name: Run gg and snapshot this run
        run: |
          docker run --rm \
            -v "${{ github.workspace }}":/work -w /work \
            ghcr.io/shyam-s00/gopher-glide:latest \
            config.yaml --profile smoke --headless --yes \
            --snap --snap-dir /work/.gg-snapshots \
            --snap-tag "${{ github.event_name == 'pull_request' && format('pr-{0}', github.event.pull_request.number) || 'main' }}"

      - name: Compare against baseline (PRs only)
        if: github.event_name == 'pull_request'
        run: |
          docker run --rm \
            -v "${{ github.workspace }}":/work -w /work \
            ghcr.io/shyam-s00/gopher-glide:latest \
            snap assert --snap-dir /work/.gg-snapshots \
            --baseline main --current "pr-${{ github.event.pull_request.number }}" \
            --latency-regression 10 --error-rate-delta 0.05 \
            --reporter md --out /work/report.md

      - name: Post PR comment
        if: github.event_name == 'pull_request'
        uses: marocchino/sticky-pull-request-comment@v2
        with:
          path: report.md

      - name: Save baseline (main only)
        if: github.event_name == 'push'
        uses: actions/cache/save@v4
        with:
          path: .gg-snapshots
          key: gg-snapshots-main
```

On pushes to `main` this tags the run `main`, and the cache step persists it as the baseline for every subsequent PR. On a PR it snapshots as `pr-<number>`, runs `gg snap assert` against the cached `main` baseline, and posts the `md`-formatted report as a sticky PR comment. `gg snap assert` exits non-zero on `REGRESSION` (or `WARN` with `--fail-on-warn`), which fails the job on its own — see [Threshold flags](/cli-reference#gg-snap-assert) for tuning `--latency-regression`, `--error-rate-delta`, `--payload-size-delta`, and `--deny-removed-fields`.

---

## Other CI systems

None of the above is GitHub-Actions-specific — it's a `docker run` invocation with two mounted volumes and an exit code. The same pattern works anywhere that can run a container and cache a directory between jobs:

- **GitLab CI** — use the Docker executor (or `docker:dind`) and GitLab's `cache:paths` in place of `actions/cache`.
- **CircleCI / Jenkins / Buildkite** — same idea: a `docker run` step, an artifact/cache mechanism for `.gg-snapshots`, and a step that fails the build on the container's exit code (which all of these already do by default for a non-zero exit).

Anywhere that isn't container-based can skip the container image and run the native `gg` binary directly — see [Getting Started](/getting-started) for installation.
