# Configuration Reference

## .http file format

`gg` uses the standard `.http` / REST Client file format. Requests are separated by `###`. Complete spec can be found [here](https://github.com/JetBrains/http-request-in-editor-spec/blob/master/spec.md)

```http
### Simple GET
GET https://httpbin.org/get
Accept: application/json
X-Request-ID: gg-001

### GET with query param
GET https://httpbin.org/get?userId=1
Accept: application/json
Cache-Control: no-cache

### POST with JSON body
POST https://httpbin.org/post
Content-Type: application/json
Accept: application/json

{
  "title": "hello",
  "userId": 1
}
```

- Supported methods: `GET`, `POST`, and any valid HTTP verb
- Headers are placed directly after the request line
- Body (for POST/PUT/PATCH) follows a blank line after the headers
- `###` separates requests; the text after `###` is an optional label
- All requests are dispatched in round-robin order across stages

---

## `config.yaml` Reference

```yaml
config:
  httpFile: "request.http"        # .http file to load (relative to config.yaml)
  jitter: 0.1                     # ±10% noise on the RPS ticker; 0 disables (default: 0)
  time_scale: 1.0                 # stage clock multiplier; 2.0 = run 2× faster (default: 1.0)
  prometheus: false               # (planned) expose /metrics endpoint
  breaker_threshold_pct: 20.0     # (planned) circuit-breaker error-rate threshold %

stages:
  - name: "Warm Up"               # optional; inferred from shape if omitted
    duration: 10s                 # how long this stage runs
    target_rps: 50                # RPS target at the end of this stage

  - duration: 30s
    target_rps: 50                # same as previous → Sustain

  - duration: 0s
    target_rps: 200               # duration 0 → instant Spike (no interpolation)

  - duration: 10s
    target_rps: 200               # hold after spike → Sustain

  - duration: 10s
    target_rps: 0                 # ramp down → cool-down
```

### Stage inference rules

When `name` is omitted, `gg` infers the display label from the stage shape:

| Shape                             | Inferred label |
|-----------------------------------|----------------|
| `target_rps` higher than previous | **Ramp Up**    |
| `target_rps` same as previous     | **Sustain**    |
| `target_rps` lower than previous  | **Ramp Down**  |
| `duration: 0s`                    | **Spike**      |
| `target_rps: 0`                   | **Ramp Down**  |

### `jitter`

Adds symmetric `±N%` noise to the inter-request interval so traffic looks organic. For example `jitter: 0.1` varies each interval by ±10%. The average rate is unchanged.

### `time_scale`

Compresses the stage clock by a multiplier. Useful for local testing:

```yaml
time_scale: 10   # a 10-minute plan finishes in 1 minute
```

---

## Multi-stage load profiles

`gg` uses **implicit ramping** — stage defines a `target_rps` and `duration`. The engine automatically interpolates (LERP) from the previous stage's rate to the new target. You never specify the "ramp type" explicitly; it is inferred from the numbers.

```yaml
stages:
  # Ramp Up: 0 → 100 RPS over 30s
  - duration: 30s
    target_rps: 100

  # Sustain: hold 100 RPS for 1 minute
  - duration: 1m
    target_rps: 100

  # Spike: jump instantly to 500 RPS
  - duration: 0s
    target_rps: 500

  # Sustain spike for 15s
  - duration: 15s
    target_rps: 500

  # Ramp Down: 500 → 0 RPS over 30s
  - duration: 30s
    target_rps: 0
```

The TUI timeline visualizes the entire plan before and during the run, with a live cursor showing the current position and an RPS marker per block showing the actual throughput achieved.
