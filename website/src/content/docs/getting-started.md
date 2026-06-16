---
title: "Getting Started"
description: "Install gg and run your first traffic simulation in under 30 seconds."
---

Get up and running with `gg` in three steps.

## 1. Install

### Homebrew (macOS / Linux)

```bash
brew install shyam-s00/tap/gg
```

### Manual download

Prebuilt binaries for macOS, Linux, and Windows are available on the [GitHub Releases](https://github.com/shyam-s00/gopher-glide/releases/latest) page.

## 2. Point it at any `.http` file

Create a simple `api.http` file (or use an existing one from your IDE):

```http
GET http://localhost:8080/health
Accept: application/json
```

## 3. Run the simulation

Use a built-in profile to automatically shape the traffic (ramping up, sustaining, and cooling down):

```bash
gg --profile flash-sale --http-file api.http
```

You'll drop straight into the interactive Chaos TUI, where you can monitor latencies and adjust load in real-time with the arrow keys.

---

Ready for more? Explore [Load Profiles](/profiles) to tune RPS and duration, or dive into [the TUI](/tui) to learn the controls.
