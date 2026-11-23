# gg config file (YAML)

A config file selects the `.http` file and describes the load shape. All keys are optional except
`config.httpFile` and `stages` (unless a profile supplies the stages; see `gg://profiles/catalog`).

```yaml
config:
  httpFile: "requests.http"    # required; resolved relative to this file's directory
  jitter: 0.1                  # 0..1; ±10% random noise on the request pacing (default 0)
  time_scale: 1.0              # stage clock multiplier; 2.0 runs the plan twice as fast (default 1)

snap:                          # optional; tunes behavioural snapshots
  sample_rate: 0.05            # 0..1; fraction of responses sampled for schema inference
  max_samples: 200             # per-endpoint sample cap
  max_body_kb: 0               # per-endpoint stored-body budget in KB (0 = no byte limit)

stages:
  - name: "Warm up"            # optional label
    duration: 30s              # Go duration: 500ms, 30s, 5m, 1h
    target_rps: 50             # the rate reached at the end of this stage
  - duration: 2m
    target_rps: 50             # same as previous: hold
  - duration: 0s
    target_rps: 200            # zero duration: instant jump
  - duration: 1m
    target_rps: 0              # ramp down to zero
```

## Stages

Each stage moves linearly from the previous stage's `target_rps` to its own over `duration`
(the first stage starts from 0). The shape is implied by the numbers:

| Stage | Meaning |
|---|---|
| `target_rps` above previous | ramp up |
| `target_rps` equal to previous | sustain |
| `target_rps` below previous | ramp down |
| `duration: 0s` | instant step |

`duration` must not be negative and `target_rps` must not be negative.
The run lasts the sum of the durations divided by `time_scale`.

## Validation rules

- `config.httpFile` must be set; `stages` must have at least one entry.
- `jitter` is 0 to 1; `time_scale` is not negative.
- `snap.sample_rate` is 0 to 1; `snap.max_samples` and `snap.max_body_kb` are not negative.

## Profiles instead of stages

Instead of hand-writing `stages`, a run can use a named profile (a reusable traffic shape scaled
to a chosen peak RPS and duration). See `gg://profiles/catalog` for the available names.
