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

## The Hive Engine 🐝
**A modern architecture for massive scale.** While older tools rely on heavy OS threads or memory-intensive Virtual Machines, the newly released Hive Engine (v0.9.0) flips the paradigm with a pure-Go Actor Model.

By isolating each concurrent connection into an ultra-lightweight Goroutine and utilizing lock-free message passing, Gopher-Glide bypasses traditional memory and scheduling bottlenecks. A single instance can now easily generate **over 50,000 RPS** on a standard laptop, laying the groundwork for fully stateful, chained API journeys coming in v1.0. Check out the full [Performance Benchmarks](BENCHMARKS.md) to see how it scales.

</div>
<div class="tx-feature-visual" markdown="1">

```mermaid
graph TD
    Queen[👑 The Queen] -->|Micro-batches| Hatchery[🥚 The Hatchery]
    Hatchery --> Actor1[🐝 Actor Goroutine]
    Hatchery --> Actor2[🐝 Actor Goroutine]
    Actor1 -. API Request .-> API
    Actor2 -. API Request .-> API
```

</div>
</div>

<div class="tx-feature-row reverse" markdown="1">
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
    client.global.set(
        "token", response.body.token
    );
%}

### Fetch user profile using token
GET http://api.example.com/profile
Authorization: Bearer {{token}}
```

</div>
</div>

<div class="tx-feature-row" markdown="1">
<div class="tx-feature-text" markdown="1">

## Zero-Config Profiles
Skip writing complex YAML configs. Use built-in patterns like `--profile flash-sale` or `--profile ddos` to generate standard industry traffic shapes instantly.

Perfect for quick validation or CI/CD pipelines where you don't want to maintain external configuration files.

</div>
<div class="tx-feature-visual" markdown="1">

```bash
$ gg --hive-engine \
    --http-file checkout.http \
    --profile flash-sale

[Stages]
1. Ramp Up: 0 -> 1000 RPS (30s)
2. Sustain: 1000 RPS (2m)
3. Cool Down: 1000 -> 0 RPS (30s)

[Status] Running Stage 2 (Sustain)...
```

</div>
</div>

<div class="tx-feature-row reverse" markdown="1">
<div class="tx-feature-text" markdown="1">

## Interactive Chaos TUI
Adjust traffic in real-time using the interactive TUI. Bias RPS up or down using your arrow keys, and watch the beautiful terminal UI react instantly. 

Combined with the `--snap` flag, you can record behavioral snapshots of your API's latency, status distributions, and inferred JSON schemas.

</div>
<div class="tx-feature-visual" markdown="1">

```bash
# Adjust traffic dynamically during a run
$ gg --hive-engine \
    --http-file api.http \
    --profile sustain

[↑] Increase Load (+50 RPS)
[↓] Decrease Load (-50 RPS)
[Q] Quit & Generate Report
```

</div>
</div>

<div class="tx-feature-row" markdown="1">
<div class="tx-feature-text" markdown="1">

## Semantic Diffing 🔬
**Know *why* your API broke, not just when.** Traditional tools tell you P99 latency spiked. `gg snap diff` tells you it spiked because a developer accidentally injected a 2MB blob into the JSON payload. 

By continuously sampling traffic, Gopher-Glide infers your API's schema in real-time. Compare two snapshots side-by-side to instantly spot missing fields, type changes, or massive payload bloat.

</div>
<div class="tx-feature-visual" markdown="1">

```text
GET:http://localhost:8080/fast-get  [❌ REGRESSION]

Latency Deltas      Payload Deltas      Error & Status
 P50:    +100.0%     Avg:    +100.0%     Error: -100.00 pp
 P95:    +100.0%     P95:    +100.0%
 P99:   +5200.0%     Max:    +100.0%     200:   +100.0 pp
 Max:    +785.7%

Schema Changes
 + active          added  boolean  100% STABLE
 + email           added  string   100% STABLE
 + metadata        added  object    18% RARE
 + metadata.source added  string    18% RARE
```

</div>
</div>

<div class="tx-feature-row reverse" markdown="1">
<div class="tx-feature-text" markdown="1">

## Automated CI/CD Gates 🚦
**Don't let regressions reach production.** Gopher-Glide is built for more than just local chaos testing. Drop it directly into your CI/CD pipelines to enforce hard limits on performance and payload shapes.

Use `gg snap assert` to automatically break the build if a Pull Request spikes P99 latency by 10% or removes a critical field from your JSON schema.

</div>
<div class="tx-feature-visual" markdown="1">

```yaml
jobs:
  api-regression:
    runs-on: ubuntu-latest
    container: shyam-s00/gopher-glide:latest
    steps:
      - name: Run API Simulator
        run: |
          gg --hive-engine \
            --http-file api.http \
            --snap --snap-tag ${{ github.sha }} \
            --headless

      - name: Enforce API Contracts
        run: |
          gg snap assert \
            --baseline main \
            --current ${{ github.sha }} \
            --latency-regression 10 \
            --deny-removed-fields
```

</div>
</div>

<div class="tx-upcoming-banner" markdown="1">
<h3>🚀 Gopher-Glide v0.9.0: The Hive Engine</h3>
<p>Experience massive concurrency with the new lock-free Actor Model. <b>Coming in v1.0:</b> Full stateful simulation, chained multi-step user journeys, and complex persona behaviors.</p>
</div>

<div class="tx-steps-section" style="background: transparent; padding-top: 2rem; padding-bottom: 0;" markdown="1">
<h2>Why Gopher-Glide?</h2>
<p style="margin-bottom: 2rem; color: var(--md-default-fg-color--light);">Feature comparison against traditional load testing tools.</p>

| Feature | Gopher-Glide (gg) | k6 | Locust | JMeter |
|---------|-------------------|----|--------|--------|
| **Scripting Language** | IDE `.http` (Zero Code) | JavaScript | Python | XML / UI |
| **IDE Integration** | ✅ Native Plugin | ❌ External | ❌ External | ❌ External |
| **Pre-built Profiles** | ✅ Built-in Patterns | ❌ Scripted | ❌ Scripted | ❌ Configured |
| **API Snapshots** | ✅ `--snap` | ❌ No | ❌ No | ❌ No |
| **Real-time TUI** | ✅ Yes (Director) | ❌ No | ❌ No | ❌ No |
| **CI/CD Native** | ✅ Yes (Headless) | ✅ Yes | ✅ Yes | ✅ Yes |
| **Stateful Simulation** | ⏳ Coming (v1.0) | ✅ Yes | ✅ Yes | ✅ Yes |
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
<code class="tx-step-code">gg --hive-engine \<br>&nbsp;&nbsp;--http-file api.http \<br>&nbsp;&nbsp;--profile sustain</code>
</div>
</div>
</div>

<div class="tx-steps-section" style="background: transparent; padding-top: 2rem;" markdown="1">
<h2>Frequently Asked Questions</h2>
<p style="margin-bottom: 2rem; color: var(--md-default-fg-color--light);">Everything you need to know about Gopher-Glide.</p>

<details class="faq">
  <summary>What is an API traffic simulator?</summary>
  <p>Unlike traditional <strong>API load testing tools</strong> that blindly spam endpoints, a traffic simulator like Gopher-Glide creates stateful, multi-step user journeys. It mimics human think-time and realistic behaviors, giving you high-fidelity insights into how your backend actually performs under real-world pressure.</p>
</details>

<details class="faq">
  <summary>Is Gopher-Glide a k6 or JMeter alternative?</summary>
  <p>Yes. If you are looking for a modern, <strong>open-source alternative to k6, JMeter, or Locust</strong>, Gopher-Glide offers a zero-code approach. Instead of writing complex JavaScript or Python scripts, you reuse your existing <code>.http</code> REST Client files directly from your IDE.</p>
</details>

<details class="faq">
  <summary>How much load can a single instance generate?</summary>
  <p>Thanks to the new pure-Go lock-free Hive Engine, a single Gopher-Glide instance can comfortably push <strong>over 50,000 RPS (Requests Per Second)</strong> on modern hardware without exhausting system memory or triggering garbage collection pauses.</p>
</details>

<details class="faq">
  <summary>Can I run this in my CI/CD pipeline?</summary>
  <p>Absolutely. You can use our official Docker image (<code>docker pull shyam-s00/gopher-glide</code>) to easily drop it into any CI/CD workflow like GitHub Actions or GitLab CI. By passing the <code>--headless</code> flag, Gopher-Glide outputs structured data instead of the UI. You can even use <code>gg snap assert</code> to create automated regression gates that break the build if API latency spikes.</p>
</details>
</div>

<div class="tx-cta-banner">
  <div class="tx-cta-title">Stop testing load. Start simulating reality.</div>
  <p>Open-source, highly concurrent, and built in Go.</p>
  <a href="https://github.com/shyam-s00/gopher-glide" class="md-button md-button--primary">Star us on GitHub ⭐</a>
</div>

</div>
