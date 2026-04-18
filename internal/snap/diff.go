package snap

import "sort"

// ── Public types ──────────────────────────────────────────────────────────────

// DiffResult is the complete behavioral comparison between two snapshots.
// Endpoints are sorted deterministically by ID so output is stable across runs.
type DiffResult struct {
	Baseline  SnapMeta       `json:"baseline"`
	Current   SnapMeta       `json:"current"`
	Endpoints []EndpointDiff `json:"endpoints"`
}

// EndpointDiff compares a single endpoint across two snapshots.
// BaselineOnly and CurrentOnly are mutually exclusive; when both are false the
// endpoint was observed in both snapshots and all delta fields are populated.
type EndpointDiff struct {
	ID string `json:"id"`

	// BaselineOnly is true when the endpoint exists in the baseline but not in
	// the current snapshot (the endpoint may have been removed or renamed).
	BaselineOnly bool `json:"baseline_only,omitempty"`

	// CurrentOnly is true when the endpoint is new in the current snapshot
	// (it was not present in the baseline).
	CurrentOnly bool `json:"current_only,omitempty"`

	// Delta fields — only meaningful when BaselineOnly and CurrentOnly are false.
	LatencyDelta   LatencyDelta       `json:"latency_delta"`
	PayloadDelta   PayloadSizeDelta   `json:"payload_delta"`
	StatusDelta    map[string]float64 `json:"status_delta"`     // per-code: current − baseline
	ErrorRateDelta float64            `json:"error_rate_delta"` // current − baseline
	SchemaChanges  []FieldChange      `json:"schema_changes,omitempty"`

	// Verdict is the summary judgement for this endpoint.
	Verdict DiffVerdict `json:"verdict"`
}

// LatencyDelta holds percentage changes in latency percentiles.
// Positive values mean the current snapshot is slower.
type LatencyDelta struct {
	P50PctChange float64 `json:"p50_pct_change"`
	P95PctChange float64 `json:"p95_pct_change"`
	P99PctChange float64 `json:"p99_pct_change"`
	MaxPctChange float64 `json:"max_pct_change"`
}

// PayloadSizeDelta holds percentage changes in response body size metrics.
// Positive values mean the current snapshot returns larger bodies.
type PayloadSizeDelta struct {
	AvgPctChange float64 `json:"avg_pct_change"`
	P95PctChange float64 `json:"p95_pct_change"`
	MaxPctChange float64 `json:"max_pct_change"`
}

// FieldChange describes a single schema-level change between two snapshots.
type FieldChange struct {
	Path          string          `json:"path"`
	Kind          FieldChangeKind `json:"kind"`
	BaseType      string          `json:"base_type,omitempty"`
	CurrType      string          `json:"curr_type,omitempty"`
	BasePresence  float64         `json:"base_presence,omitempty"`
	CurrPresence  float64         `json:"curr_presence,omitempty"`
	BaseStability string          `json:"base_stability,omitempty"`
	CurrStability string          `json:"curr_stability,omitempty"`
}

// FieldChangeKind classifies the nature of a schema-level change.
type FieldChangeKind string

const (
	// FieldAdded means the field is present in current but absent in baseline.
	FieldAdded FieldChangeKind = "added"
	// FieldRemoved means the field was present in baseline but is gone in current.
	FieldRemoved FieldChangeKind = "removed"
	// FieldTypeChanged means the dominant JSON type for the field changed.
	FieldTypeChanged FieldChangeKind = "type_changed"
	// FieldStabilityChanged means the field's presence fraction crossed a
	// stability threshold (e.g. STABLE → VOLATILE).
	FieldStabilityChanged FieldChangeKind = "stability_changed"
)

// DiffVerdict is the summary judgement for an endpoint comparison.
type DiffVerdict string

const (
	// VerdictPass means no thresholds were breached.
	VerdictPass DiffVerdict = "PASS"
	// VerdictWarn means a non-critical threshold was breached (e.g. payload
	// size grew significantly, or the endpoint is new / missing).
	VerdictWarn DiffVerdict = "WARN"
	// VerdictRegression means a critical threshold was breached (e.g. P99
	// latency increased beyond the configured limit, or error rate spiked).
	VerdictRegression DiffVerdict = "REGRESSION"
)

// DiffOptions controls the thresholds used by the diff engine to classify
// endpoint changes. Use DefaultDiffOptions() for sensible production defaults.
type DiffOptions struct {
	// LatencyP99RegressionPct is the percentage increase in P99 latency that
	// triggers a REGRESSION verdict. Default: 20 %.
	LatencyP99RegressionPct float64

	// ErrorRateDeltaThreshold is the absolute increase in error rate (0–1)
	// that triggers a REGRESSION verdict. Default: 0.05 (5 pp).
	ErrorRateDeltaThreshold float64

	// PayloadSizeAvgPctThreshold is the percentage increase in average payload
	// size that triggers a WARN verdict. Default: 50 %.
	PayloadSizeAvgPctThreshold float64

	// DenyRemovedFields upgrades a removed schema field from WARN to REGRESSION.
	// Useful in CI pipelines where consumers depend on every field being stable.
	DenyRemovedFields bool
}

// DefaultDiffOptions returns a DiffOptions with sensible production thresholds.
func DefaultDiffOptions() DiffOptions {
	return DiffOptions{
		LatencyP99RegressionPct:    20,
		ErrorRateDeltaThreshold:    0.05,
		PayloadSizeAvgPctThreshold: 50,
	}
}

// ── Engine ────────────────────────────────────────────────────────────────────

// Diff compares baseline and current snapshots using opts as regression
// thresholds. The returned DiffResult contains one EndpointDiff per unique
// endpoint ID across both snapshots, sorted alphabetically by ID.
func Diff(baseline, current *Snapshot, opts DiffOptions) DiffResult {
	result := DiffResult{
		Baseline: baseline.Meta,
		Current:  current.Meta,
	}

	baseMap := indexEndpoints(baseline.Endpoints)
	currMap := indexEndpoints(current.Endpoints)

	// Endpoints present in baseline (may also be in current).
	for id, base := range baseMap {
		curr, exists := currMap[id]
		if !exists {
			result.Endpoints = append(result.Endpoints, EndpointDiff{
				ID:           id,
				BaselineOnly: true,
				Verdict:      VerdictWarn,
			})
			continue
		}
		result.Endpoints = append(result.Endpoints, diffEndpoint(id, base, curr, opts))
	}

	// Endpoints only in current (new since baseline).
	for id := range currMap {
		if _, exists := baseMap[id]; !exists {
			result.Endpoints = append(result.Endpoints, EndpointDiff{
				ID:          id,
				CurrentOnly: true,
				Verdict:     VerdictWarn,
			})
		}
	}

	// Deterministic output order regardless of map iteration.
	sort.Slice(result.Endpoints, func(i, j int) bool {
		return result.Endpoints[i].ID < result.Endpoints[j].ID
	})

	return result
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func indexEndpoints(eps []EndpointSnap) map[string]EndpointSnap {
	m := make(map[string]EndpointSnap, len(eps))
	for _, ep := range eps {
		m[ep.ID] = ep
	}
	return m
}

func diffEndpoint(id string, base, curr EndpointSnap, opts DiffOptions) EndpointDiff {
	d := EndpointDiff{
		ID:             id,
		LatencyDelta:   diffLatency(base.Latency, curr.Latency),
		PayloadDelta:   diffPayload(base.PayloadSize, curr.PayloadSize),
		StatusDelta:    diffStatusDist(base.StatusDist, curr.StatusDist),
		ErrorRateDelta: curr.ErrorRate - base.ErrorRate,
		SchemaChanges:  diffSchema(base.Schema, curr.Schema),
	}
	d.Verdict = calcVerdict(d, opts)
	return d
}

func diffLatency(base, curr LatencyStats) LatencyDelta {
	return LatencyDelta{
		P50PctChange: pctChange(base.P50, curr.P50),
		P95PctChange: pctChange(base.P95, curr.P95),
		P99PctChange: pctChange(base.P99, curr.P99),
		MaxPctChange: pctChange(base.Max, curr.Max),
	}
}

func diffPayload(base, curr PayloadSizeStats) PayloadSizeDelta {
	return PayloadSizeDelta{
		AvgPctChange: pctChange(base.Avg, curr.Avg),
		P95PctChange: pctChange(base.P95, curr.P95),
		MaxPctChange: pctChange(base.Max, curr.Max),
	}
}

func diffStatusDist(base, curr map[string]float64) map[string]float64 {
	// Union of all status codes seen in either snapshot.
	all := make(map[string]struct{}, len(base)+len(curr))
	for k := range base {
		all[k] = struct{}{}
	}
	for k := range curr {
		all[k] = struct{}{}
	}
	delta := make(map[string]float64, len(all))
	for code := range all {
		delta[code] = curr[code] - base[code]
	}
	return delta
}

// diffSchema computes field-level changes between two SchemaSnapshots.
// Either argument may be nil (no schema was captured for that run).
func diffSchema(base, curr *SchemaSnapshot) []FieldChange {
	var baseFields, currFields map[string]FieldSchema
	if base != nil {
		baseFields = base.Fields
	}
	if curr != nil {
		currFields = curr.Fields
	}
	if len(baseFields) == 0 && len(currFields) == 0 {
		return nil
	}

	var changes []FieldChange

	// Scan baseline fields: detect removals, type changes, and stability shifts.
	for path, bf := range baseFields {
		cf, exists := currFields[path]
		if !exists {
			changes = append(changes, FieldChange{
				Path:          path,
				Kind:          FieldRemoved,
				BaseType:      bf.Type,
				BasePresence:  bf.Presence,
				BaseStability: bf.Stability,
			})
			continue
		}
		if bf.Type != cf.Type {
			changes = append(changes, FieldChange{
				Path:          path,
				Kind:          FieldTypeChanged,
				BaseType:      bf.Type,
				CurrType:      cf.Type,
				BasePresence:  bf.Presence,
				CurrPresence:  cf.Presence,
				BaseStability: bf.Stability,
				CurrStability: cf.Stability,
			})
		} else if bf.Stability != cf.Stability {
			changes = append(changes, FieldChange{
				Path:          path,
				Kind:          FieldStabilityChanged,
				BaseType:      bf.Type,
				CurrType:      cf.Type,
				BasePresence:  bf.Presence,
				CurrPresence:  cf.Presence,
				BaseStability: bf.Stability,
				CurrStability: cf.Stability,
			})
		}
	}

	// Scan current fields: detect additions.
	for path, cf := range currFields {
		if _, exists := baseFields[path]; !exists {
			changes = append(changes, FieldChange{
				Path:          path,
				Kind:          FieldAdded,
				CurrType:      cf.Type,
				CurrPresence:  cf.Presence,
				CurrStability: cf.Stability,
			})
		}
	}

	// Sort for deterministic output (path first, then kind as tiebreaker).
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path != changes[j].Path {
			return changes[i].Path < changes[j].Path
		}
		return changes[i].Kind < changes[j].Kind
	})
	return changes
}

// calcVerdict computes the overall DiffVerdict for a single endpoint diff.
//
// Escalation ladder (highest wins):
//
//	PASS → WARN → REGRESSION
func calcVerdict(d EndpointDiff, opts DiffOptions) DiffVerdict {
	verdict := VerdictPass

	escalate := func(v DiffVerdict) {
		if v == VerdictRegression || (v == VerdictWarn && verdict == VerdictPass) {
			verdict = v
		}
	}

	// P99 latency regression.
	if d.LatencyDelta.P99PctChange > opts.LatencyP99RegressionPct {
		escalate(VerdictRegression)
	}

	// Error rate spike.
	if d.ErrorRateDelta > opts.ErrorRateDeltaThreshold {
		escalate(VerdictRegression)
	}

	// Schema changes.
	for _, fc := range d.SchemaChanges {
		switch fc.Kind {
		case FieldRemoved:
			if opts.DenyRemovedFields {
				escalate(VerdictRegression)
			} else {
				escalate(VerdictWarn)
			}
		case FieldTypeChanged:
			// A type change is always a potential breaking change → REGRESSION.
			escalate(VerdictRegression)
		case FieldAdded, FieldStabilityChanged:
			escalate(VerdictWarn)
		}
	}

	// Payload size growth → WARN (unless already escalated to REGRESSION).
	if d.PayloadDelta.AvgPctChange > opts.PayloadSizeAvgPctThreshold {
		escalate(VerdictWarn)
	}

	return verdict
}

// pctChange returns the percentage change from base to curr.
//
//	pctChange(100, 120) → 20.0   (20 % increase)
//	pctChange(100,  80) → −20.0  (20 % decrease)
//	pctChange(0,     0) → 0.0    (no change)
//	pctChange(0,    10) → 100.0  (base is zero — treated as infinite increase)
func pctChange(base, curr float64) float64 {
	if base == 0 {
		if curr == 0 {
			return 0
		}
		return 100 // non-zero curr from zero base → represent as 100 % increase
	}
	return (curr - base) / base * 100
}
