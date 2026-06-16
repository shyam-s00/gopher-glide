---
title: "Load Profiles"
---
# Load Profiles

The **Profiles** feature allows developers to execute predefined, real-world load testing scenarios (e.g., flash-sales, DDoS attacks) without needing to manually author configuration files. It shifts `gg` towards a **Zero-Config Default** philosophy.

Profiles define the *shape* of the traffic abstractly using percentages, rather than hardcoded limits. This ensures you can scale any profile to your infrastructure limits while retaining the same behavioral characteristics.

---

## Using Profiles

You can run complex tests with a single command without needing a `config.yaml` file:

```bash
gg --profile flash-sale --http-file target.http
```

### Scaling Profiles

Profiles are flexible. You can scale the peak RPS (Y-Axis) and stretch the time (X-Axis) of any profile via CLI flags:

- `--peak-rps` or `-r`: Sets the maximum RPS for the profile.
- `--duration` or `-d`: Stretches or compresses the total duration of the profile.

```bash
# Run a flash-sale with a 5000 RPS peak over 10 minutes
gg --profile flash-sale --peak-rps 5000 --duration 10m --http-file target.http
```

---

## Out-of-the-Box Profiles

`gg` ships with 21 predefined profiles categorized by use-case.

### 🛍️ E-Commerce & High-Demand Events
- **`flash-sale`**: Massive instant spike from 0 to peak, hold for 3m, instant drop.
- **`black-friday`**: Extended heavy baseline load with unpredictable sharp spikes.
- **`ticket-release`**: Instantaneous, extreme spike held for a narrow 1m window.
- **`inventory-drop`**: High immediate load with a slow tail-off over 15m.

### 📊 Standard Testing & CI/CD
- **`canary`**: Very brief, very low RPS (post-deployment verification).
- **`smoke`**: Quick health check (10 RPS / 10s).
- **`load`**: Standard baseline test (gradual ramp up, hold 1m, gradual ramp down).
- **`stress`**: Aggressive, steep ramp up designed to hit limits.
- **`soak`**: Sustained, moderate load over a long duration (1h+).
- **`endurance`**: Soak test but at a dangerously high capacity (2h).

### 🌪️ Resilience & Chaos
- **`ddos`**: Sustained high-volume attack simulation (no ramp).
- **`spike`**: Quick jump and drop to test queue buffering.
- **`burst`**: Repeating series of short spikes.
- **`retry-storm`**: Fast repeating spikes simulating aggressive client retries.
- **`chaos`**: Extremely high `jitter` on a moderate RPS. (WIP)

### 📈 Auto-Scaling & Infrastructure
- **`step-up`**: Staircase ramp up to gracefully find breaking points.
- **`wave`**: Oscillating RPS (sine wave) to test auto-scaling elasticity.
- **`scale-down`**: Starts high, slowly ramps down to zero over 10m.

### 🛠️ Specialized Traffic
- **`crawler`**: Simulates search engine crawlers (steady, moderate).
- **`trickle`**: Very low, constant RPS to test idle timeouts.
- **`warm-up`**: Very slow ramp up over 5 mins for JIT/cache warming.

---

## Profile Management Commands

`gg` provides subcommands to explore and manage profiles.

### Listing Profiles

To see all available profiles (both built-in and custom):

```bash
gg profile list
```

### Visual Previews

To see an ASCII chart of the profile's traffic curve before execution:

```bash
gg profile view <name>
```

*Note: The CLI also automatically renders the ASCII chart before execution so you know exactly what is about to happen.*

### Exporting & Custom Profiles

You can export a built-in profile to your local configuration folder (`~/.config/gg/profiles/`) to act as a starting point for customization:

```bash
gg profile export flash-sale
```

**Important:** You must rename the exported file. Built-in profile names are reserved. If a file in your custom profiles directory collides with a built-in name, `gg` will error.

Once renamed (e.g., `my-custom-sale.yaml`), you can use it like any other profile:

```bash
gg --profile my-custom-sale --http-file target.http
```

### Snapshots Compatibility

Taking a behavioral snapshot (`--snap`) of how a system degrades under a profile (like `ddos`) is incredibly valuable. The snapshot recorder will automatically capture the exact, scaled traffic shape that was executed, storing the `ProfileName` and `ProfileScale` in the metadata.
