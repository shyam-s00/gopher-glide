---
hide:
  - navigation
  - toc
---

<div class="tx-container" markdown="1">

<div class="tx-hero">
  <img src="assets/ggToolIcon_dark.svg" alt="Gopher-Glide Logo" width="280" style="margin-bottom: 0.5rem;" class="tx-hero-logo">
  <h1>High-fidelity API traffic simulation from your IDE.</h1>
  <p>Gopher-Glide (gg) is an open-source, high-fidelity API traffic simulator. Move beyond brute-force load testing with native IDE integration, zero-config profiles, and an interactive TUI that lets you inject chaos in real-time.</p>
  
  <div class="tx-hero-buttons">
    <a href="#quick-start" class="md-button md-button--primary">Get Started</a>
    <a href="https://github.com/shyam-s00/gopher-glide" class="md-button">View on GitHub</a>
  </div>
</div>

<div class="tx-feature-row" markdown="1">
<div class="tx-feature-text" markdown="1">

## Code-to-Load Pipeline
Reuse your existing `.http` REST Client files directly. Featuring a native JetBrains IDE plugin, there is no need to rewrite requests in JavaScript or Python. Just point `gg` at your file and go.

</div>
<div class="tx-feature-visual" markdown="1">

```http
### Simulated User Journey
POST http://api.example.com/login
Content-Type: application/json

{ "user": "tester", "pass": "secure" }

> {%
    client.global.set("token", response.body.token);
%}

### Fetch user profile using token
GET http://api.example.com/profile
Authorization: Bearer {{token}}
```

</div>
</div>

<div class="tx-feature-row reverse" markdown="1">
<div class="tx-feature-text" markdown="1">

## Zero-Config Profiles
Skip writing complex YAML configs. Use built-in patterns like `--profile flash-sale` or `--profile ddos` to generate standard industry traffic shapes instantly.

Perfect for quick validation or CI/CD pipelines where you don't want to maintain external configuration files.

</div>
<div class="tx-feature-visual" markdown="1">

```bash
$ gg run checkout.http --profile flash-sale

[Stages]
1. Ramp Up: 0 -> 1000 RPS (30s)
2. Sustain: 1000 RPS (2m)
3. Cool Down: 1000 -> 0 RPS (30s)

[Status] Running Stage 2 (Sustain)...
```

</div>
</div>

<div class="tx-feature-row" markdown="1">
<div class="tx-feature-text" markdown="1">

## Interactive Chaos TUI
Adjust traffic in real-time using the interactive TUI. Bias RPS up or down using your arrow keys, and watch the beautiful terminal UI react instantly. 

Combined with `gg snap`, you can record behavioral snapshots of your API's latency, status distributions, and inferred JSON schemas.

</div>
<div class="tx-feature-visual" markdown="1">

```bash
# Adjust traffic dynamically during a run
$ gg run api.http --profile sustain

[↑] Increase Load (+50 RPS)
[↓] Decrease Load (-50 RPS)
[Q] Quit & Generate Report
```

</div>
</div>
<div class="tx-upcoming-banner" markdown="1">
<h3>🚧 Planned Features</h3>
<p>We are expanding Gopher-Glide into a full-scale traffic simulator. Planned capabilities include <b>Chaos Engineering</b>, a <b>Distributed Simulation Mesh</b>, and <b>Auto-Pilot</b> scaling.</p>
</div>

<div class="tx-steps-section" style="background: transparent; padding-top: 2rem; padding-bottom: 0;" markdown="1">
<h2>Why Gopher-Glide?</h2>
<p style="margin-bottom: 2rem; color: var(--md-default-fg-color--light);">Feature comparison against traditional load testing tools.</p>

| Feature | Gopher-Glide (gg) | k6 | Locust | JMeter |
|---------|-------------------|----|--------|--------|
| **Scripting Language** | IDE `.http` (Zero Code) | JavaScript | Python | XML / UI |
| **IDE Integration** | ✅ Native Plugin | ❌ External | ❌ External | ❌ External |
| **Pre-built Profiles** | ✅ Built-in Patterns | ❌ Scripted | ❌ Scripted | ❌ Configured |
| **API Snapshots** | ✅ `gg snap` | ❌ No | ❌ No | ❌ No |
| **Real-time TUI** | ✅ Yes (Director) | ❌ No | ❌ No | ❌ No |
| **CI/CD Native** | ✅ Yes (Headless) | ✅ Yes | ✅ Yes | ✅ Yes |
| **Stateful Simulation** | ✅ Yes (Hive Engine) | ✅ Yes | ✅ Yes | ✅ Yes |
| **Memory Footprint** | Extremely Low | Medium | High | High |

</div>
<div class="tx-steps-section" markdown="1" id="quick-start">
<h2>Three steps. Zero config.</h2>

<div class="tx-steps-grid" markdown="1">
<div class="tx-step-card" markdown="1">
<div class="tx-step-number">01</div>
<h3>Install</h3>
<p>Get the binary via Homebrew in seconds.</p>
<code class="tx-step-code">brew install shyam-s00/tap/gg</code>
</div>

<div class="tx-step-card" markdown="1">
<div class="tx-step-number">02</div>
<h3>Write</h3>
<p>Use your existing IDE `.http` files.</p>
<code class="tx-step-code">GET http://localhost:8080/health</code>
</div>

<div class="tx-step-card" markdown="1">
<div class="tx-step-number">03</div>
<h3>Simulate</h3>
<p>Run the traffic simulator with a built-in profile.</p>
<code class="tx-step-code">gg run api.http --profile sustain</code>
</div>
</div>
</div>

<div class="tx-cta-banner">
  <div class="tx-cta-title">Stop testing load. Start simulating reality.</div>
  <p>Open-source, highly concurrent, and built in Go.</p>
  <a href="https://github.com/shyam-s00/gopher-glide" class="md-button md-button--primary">Star us on GitHub ⭐</a>
</div>

</div>
