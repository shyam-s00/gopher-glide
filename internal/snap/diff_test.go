package snap

import (
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func makeSnap(endpoints ...EndpointSnap) *Snapshot {
	return &Snapshot{Version: 1, Endpoints: endpoints}
}

func ep(id string, p50, p95, p99, maxMs float64, errRate float64) EndpointSnap {
	return EndpointSnap{
		ID: id,
		Latency: LatencyStats{
			P50: p50, P95: p95, P99: p99, Max: maxMs,
		},
		ErrorRate:  errRate,
		StatusDist: map[string]float64{"200": 1 - errRate, "500": errRate},
	}
}

func epWithPayload(id string, latAvg, payAvg, payP95, payMax float64) EndpointSnap {
	return EndpointSnap{
		ID:          id,
		Latency:     LatencyStats{P50: latAvg, P95: latAvg * 2, P99: latAvg * 3, Max: latAvg * 4},
		PayloadSize: PayloadSizeStats{Avg: payAvg, P95: payP95, Max: payMax},
		StatusDist:  map[string]float64{"200": 1.0},
	}
}

func epWithSchema(id string, schema *SchemaSnapshot) EndpointSnap {
	return EndpointSnap{
		ID:         id,
		Latency:    LatencyStats{P50: 10, P95: 20, P99: 30, Max: 50},
		StatusDist: map[string]float64{"200": 1.0},
		Schema:     schema,
	}
}

func makeSchema(fields map[string]FieldSchema) *SchemaSnapshot {
	return &SchemaSnapshot{SampleCount: 100, Fields: fields}
}

func defaultOpts() DiffOptions { return DefaultDiffOptions() }

// ── pctChange ─────────────────────────────────────────────────────────────────

func TestPctChange_Increase(t *testing.T) {
	got := pctChange(100, 120)
	if got != 20.0 {
		t.Errorf("pctChange(100,120) = %v, want 20.0", got)
	}
}

func TestPctChange_Decrease(t *testing.T) {
	got := pctChange(100, 80)
	if got != -20.0 {
		t.Errorf("pctChange(100,80) = %v, want -20.0", got)
	}
}

func TestPctChange_NeitherZero(t *testing.T) {
	got := pctChange(0, 0)
	if got != 0 {
		t.Errorf("pctChange(0,0) = %v, want 0", got)
	}
}

func TestPctChange_BaseZeroCurrNonZero(t *testing.T) {
	got := pctChange(0, 50)
	if got != 100 {
		t.Errorf("pctChange(0,50) = %v, want 100", got)
	}
}

func TestPctChange_BaseZeroCurrZero(t *testing.T) {
	got := pctChange(0, 0)
	if got != 0 {
		t.Errorf("pctChange(0,0) = %v, want 0", got)
	}
}

// ── Diff: basic structure ─────────────────────────────────────────────────────

func TestDiff_MetaIsPreserved(t *testing.T) {
	base := makeSnap()
	base.Meta.Tag = "v1"
	curr := makeSnap()
	curr.Meta.Tag = "v2"

	result := Diff(base, curr, defaultOpts())
	if result.Baseline.Tag != "v1" {
		t.Errorf("Baseline.Tag = %q, want v1", result.Baseline.Tag)
	}
	if result.Current.Tag != "v2" {
		t.Errorf("Current.Tag = %q, want v2", result.Current.Tag)
	}
}

func TestDiff_EmptySnapshotsProduceNoEndpoints(t *testing.T) {
	result := Diff(makeSnap(), makeSnap(), defaultOpts())
	if len(result.Endpoints) != 0 {
		t.Errorf("expected 0 endpoints, got %d", len(result.Endpoints))
	}
}

func TestDiff_EndpointsSortedAlphabetically(t *testing.T) {
	base := makeSnap(ep("GET:/z", 10, 20, 30, 50, 0), ep("GET:/a", 10, 20, 30, 50, 0))
	curr := makeSnap(ep("GET:/z", 10, 20, 30, 50, 0), ep("GET:/a", 10, 20, 30, 50, 0))

	result := Diff(base, curr, defaultOpts())
	if len(result.Endpoints) < 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(result.Endpoints))
	}
	if result.Endpoints[0].ID != "GET:/a" || result.Endpoints[1].ID != "GET:/z" {
		t.Errorf("endpoints not sorted: %v, %v", result.Endpoints[0].ID, result.Endpoints[1].ID)
	}
}

// ── Diff: baseline-only / current-only ────────────────────────────────────────

func TestDiff_BaselineOnly_IsWarn(t *testing.T) {
	base := makeSnap(ep("GET:/gone", 10, 20, 30, 50, 0))
	curr := makeSnap()

	result := Diff(base, curr, defaultOpts())
	if len(result.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(result.Endpoints))
	}
	d := result.Endpoints[0]
	if !d.BaselineOnly {
		t.Error("expected BaselineOnly = true")
	}
	if d.Verdict != VerdictWarn {
		t.Errorf("expected WARN, got %v", d.Verdict)
	}
}

func TestDiff_CurrentOnly_IsWarn(t *testing.T) {
	base := makeSnap()
	curr := makeSnap(ep("GET:/new", 10, 20, 30, 50, 0))

	result := Diff(base, curr, defaultOpts())
	if len(result.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(result.Endpoints))
	}
	d := result.Endpoints[0]
	if !d.CurrentOnly {
		t.Error("expected CurrentOnly = true")
	}
	if d.Verdict != VerdictWarn {
		t.Errorf("expected WARN, got %v", d.Verdict)
	}
}

// ── Diff: latency regression ──────────────────────────────────────────────────

func TestDiff_P99Regression_AboveThreshold(t *testing.T) {
	base := makeSnap(ep("GET:/api", 10, 20, 100, 200, 0))
	// P99: 100 → 130 = 30 % increase, threshold is 20 %
	curr := makeSnap(ep("GET:/api", 10, 20, 130, 200, 0))

	result := Diff(base, curr, defaultOpts())
	d := result.Endpoints[0]
	if d.Verdict != VerdictRegression {
		t.Errorf("expected REGRESSION, got %v", d.Verdict)
	}
	if d.LatencyDelta.P99PctChange <= 0 {
		t.Errorf("P99PctChange should be positive, got %v", d.LatencyDelta.P99PctChange)
	}
}

func TestDiff_P99Regression_BelowThreshold_IsPass(t *testing.T) {
	base := makeSnap(ep("GET:/api", 10, 20, 100, 200, 0))
	// P99: 100 → 110 = 10 % increase, threshold is 20 %
	curr := makeSnap(ep("GET:/api", 10, 20, 110, 200, 0))

	result := Diff(base, curr, defaultOpts())
	d := result.Endpoints[0]
	if d.Verdict != VerdictPass {
		t.Errorf("expected PASS, got %v", d.Verdict)
	}
}

func TestDiff_LatencyImproved_IsPass(t *testing.T) {
	base := makeSnap(ep("GET:/api", 10, 20, 100, 200, 0))
	curr := makeSnap(ep("GET:/api", 8, 15, 80, 150, 0))

	result := Diff(base, curr, defaultOpts())
	if result.Endpoints[0].Verdict != VerdictPass {
		t.Errorf("expected PASS for improved latency, got %v", result.Endpoints[0].Verdict)
	}
}

// ── Diff: error rate ──────────────────────────────────────────────────────────

func TestDiff_ErrorRateSpike_IsRegression(t *testing.T) {
	// baseline 0 % → current 10 % errors (Δ = 0.10 > threshold 0.05)
	base := makeSnap(ep("POST:/api", 10, 20, 30, 50, 0.0))
	curr := makeSnap(ep("POST:/api", 10, 20, 30, 50, 0.10))

	result := Diff(base, curr, defaultOpts())
	d := result.Endpoints[0]
	if d.Verdict != VerdictRegression {
		t.Errorf("expected REGRESSION, got %v", d.Verdict)
	}
	wantDelta := 0.10
	if d.ErrorRateDelta != wantDelta {
		t.Errorf("ErrorRateDelta = %v, want %v", d.ErrorRateDelta, wantDelta)
	}
}

func TestDiff_ErrorRateReduction_IsPass(t *testing.T) {
	base := makeSnap(ep("POST:/api", 10, 20, 30, 50, 0.10))
	curr := makeSnap(ep("POST:/api", 10, 20, 30, 50, 0.02))

	result := Diff(base, curr, defaultOpts())
	if result.Endpoints[0].Verdict != VerdictPass {
		t.Errorf("expected PASS for reduced error rate, got %v", result.Endpoints[0].Verdict)
	}
}

// ── Diff: payload size ────────────────────────────────────────────────────────

func TestDiff_PayloadGrowth_AboveThreshold_IsWarn(t *testing.T) {
	// avg payload 1000 → 1600 bytes = 60 % increase, threshold is 50 %
	base := makeSnap(epWithPayload("GET:/api", 10, 1000, 1500, 3000))
	curr := makeSnap(epWithPayload("GET:/api", 10, 1600, 2000, 4000))

	result := Diff(base, curr, defaultOpts())
	d := result.Endpoints[0]
	if d.Verdict != VerdictWarn {
		t.Errorf("expected WARN for large payload growth, got %v", d.Verdict)
	}
	if d.PayloadDelta.AvgPctChange <= 50 {
		t.Errorf("AvgPctChange should be > 50, got %v", d.PayloadDelta.AvgPctChange)
	}
}

func TestDiff_PayloadGrowth_BelowThreshold_IsPass(t *testing.T) {
	// avg payload 1000 → 1200 bytes = 20 % increase, threshold is 50 %
	base := makeSnap(epWithPayload("GET:/api", 10, 1000, 1500, 3000))
	curr := makeSnap(epWithPayload("GET:/api", 10, 1200, 1700, 3200))

	result := Diff(base, curr, defaultOpts())
	if result.Endpoints[0].Verdict != VerdictPass {
		t.Errorf("expected PASS for small payload growth, got %v", result.Endpoints[0].Verdict)
	}
}

func TestDiff_PayloadShrinks_IsPass(t *testing.T) {
	base := makeSnap(epWithPayload("GET:/api", 10, 2000, 3000, 5000))
	curr := makeSnap(epWithPayload("GET:/api", 10, 1000, 1500, 2500))

	result := Diff(base, curr, defaultOpts())
	if result.Endpoints[0].Verdict != VerdictPass {
		t.Errorf("expected PASS for shrinking payload, got %v", result.Endpoints[0].Verdict)
	}
	if result.Endpoints[0].PayloadDelta.AvgPctChange >= 0 {
		t.Errorf("AvgPctChange should be negative for shrinking payload, got %v",
			result.Endpoints[0].PayloadDelta.AvgPctChange)
	}
}

func TestDiff_PayloadZeroBase_IsHandled(t *testing.T) {
	// Baseline had no payload size info (zero), current has some.
	base := makeSnap(epWithPayload("GET:/api", 10, 0, 0, 0))
	curr := makeSnap(epWithPayload("GET:/api", 10, 1000, 1500, 3000))

	result := Diff(base, curr, defaultOpts())
	d := result.Endpoints[0]
	// pctChange(0, 1000) = 100 % which exceeds the 50 % threshold → WARN
	if d.Verdict != VerdictWarn {
		t.Errorf("expected WARN when base payload is zero and current is non-zero, got %v", d.Verdict)
	}
}

// ── Diff: schema changes ──────────────────────────────────────────────────────

func TestDiff_FieldAdded_IsWarn(t *testing.T) {
	base := makeSnap(epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"id": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))
	curr := makeSnap(epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"id":    {Type: "string", Presence: 1.0, Stability: StabilityStable},
		"email": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))

	result := Diff(base, curr, defaultOpts())
	d := result.Endpoints[0]
	if d.Verdict != VerdictWarn {
		t.Errorf("expected WARN for added field, got %v", d.Verdict)
	}
	if len(d.SchemaChanges) != 1 {
		t.Fatalf("expected 1 schema change, got %d", len(d.SchemaChanges))
	}
	if d.SchemaChanges[0].Kind != FieldAdded || d.SchemaChanges[0].Path != "email" {
		t.Errorf("unexpected schema change: %+v", d.SchemaChanges[0])
	}
}

func TestDiff_FieldRemoved_WithoutDenyOption_IsWarn(t *testing.T) {
	base := makeSnap(epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"id":    {Type: "string", Presence: 1.0, Stability: StabilityStable},
		"email": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))
	curr := makeSnap(epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"id": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))

	opts := defaultOpts()
	opts.DenyRemovedFields = false
	result := Diff(base, curr, opts)
	d := result.Endpoints[0]
	if d.Verdict != VerdictWarn {
		t.Errorf("expected WARN for removed field (DenyRemovedFields=false), got %v", d.Verdict)
	}
}

func TestDiff_FieldRemoved_WithDenyOption_IsRegression(t *testing.T) {
	base := makeSnap(epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"id":    {Type: "string", Presence: 1.0, Stability: StabilityStable},
		"email": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))
	curr := makeSnap(epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"id": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))

	opts := defaultOpts()
	opts.DenyRemovedFields = true
	result := Diff(base, curr, opts)
	d := result.Endpoints[0]
	if d.Verdict != VerdictRegression {
		t.Errorf("expected REGRESSION for removed field (DenyRemovedFields=true), got %v", d.Verdict)
	}
}

func TestDiff_FieldTypeChanged_IsRegression(t *testing.T) {
	base := makeSnap(epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"count": {Type: "number", Presence: 1.0, Stability: StabilityStable},
	})))
	curr := makeSnap(epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"count": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))

	result := Diff(base, curr, defaultOpts())
	d := result.Endpoints[0]
	if d.Verdict != VerdictRegression {
		t.Errorf("expected REGRESSION for type change, got %v", d.Verdict)
	}
	if len(d.SchemaChanges) != 1 || d.SchemaChanges[0].Kind != FieldTypeChanged {
		t.Errorf("expected one FieldTypeChanged, got %+v", d.SchemaChanges)
	}
}

func TestDiff_FieldStabilityChanged_IsWarn(t *testing.T) {
	base := makeSnap(epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"role": {Type: "string", Presence: 0.96, Stability: StabilityStable},
	})))
	curr := makeSnap(epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"role": {Type: "string", Presence: 0.60, Stability: StabilityVolatile},
	})))

	result := Diff(base, curr, defaultOpts())
	d := result.Endpoints[0]
	if d.Verdict != VerdictWarn {
		t.Errorf("expected WARN for stability change, got %v", d.Verdict)
	}
	if len(d.SchemaChanges) != 1 || d.SchemaChanges[0].Kind != FieldStabilityChanged {
		t.Errorf("expected one FieldStabilityChanged, got %+v", d.SchemaChanges)
	}
}

func TestDiff_NoSchemaChanges_WhenSchemasIdentical(t *testing.T) {
	fields := map[string]FieldSchema{
		"id":   {Type: "string", Presence: 1.0, Stability: StabilityStable},
		"name": {Type: "string", Presence: 0.9, Stability: StabilityStable},
	}
	base := makeSnap(epWithSchema("GET:/api", makeSchema(fields)))
	curr := makeSnap(epWithSchema("GET:/api", makeSchema(fields)))

	result := Diff(base, curr, defaultOpts())
	if len(result.Endpoints[0].SchemaChanges) != 0 {
		t.Errorf("expected no schema changes for identical schemas, got %+v", result.Endpoints[0].SchemaChanges)
	}
}

func TestDiff_NilSchema_BaseAndCurrent_NoChanges(t *testing.T) {
	base := makeSnap(ep("GET:/api", 10, 20, 30, 50, 0))
	curr := makeSnap(ep("GET:/api", 10, 20, 30, 50, 0))

	result := Diff(base, curr, defaultOpts())
	if len(result.Endpoints[0].SchemaChanges) != 0 {
		t.Errorf("expected no schema changes when both schemas are nil")
	}
}

func TestDiff_SchemaChanges_SortedByPath(t *testing.T) {
	base := makeSnap(epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"z_field": {Type: "string", Presence: 1.0, Stability: StabilityStable},
		"a_field": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))
	curr := makeSnap(epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{})))

	result := Diff(base, curr, defaultOpts())
	changes := result.Endpoints[0].SchemaChanges
	if len(changes) < 2 {
		t.Fatalf("expected 2 schema changes, got %d", len(changes))
	}
	if changes[0].Path > changes[1].Path {
		t.Errorf("schema changes not sorted by path: %v, %v", changes[0].Path, changes[1].Path)
	}
}

// ── Diff: status distribution ─────────────────────────────────────────────────

func TestDiff_StatusDelta_Computed(t *testing.T) {
	base := makeSnap(EndpointSnap{
		ID:         "GET:/api",
		StatusDist: map[string]float64{"200": 0.97, "500": 0.03},
		Latency:    LatencyStats{P50: 10, P95: 20, P99: 30, Max: 50},
	})
	curr := makeSnap(EndpointSnap{
		ID:         "GET:/api",
		StatusDist: map[string]float64{"200": 0.90, "500": 0.08, "503": 0.02},
		Latency:    LatencyStats{P50: 10, P95: 20, P99: 30, Max: 50},
	})

	result := Diff(base, curr, defaultOpts())
	d := result.Endpoints[0]

	// 500: 0.08 - 0.03 = +0.05
	want500 := 0.08 - 0.03
	if got := d.StatusDelta["500"]; abs(got-want500) > 1e-9 {
		t.Errorf("StatusDelta[500] = %v, want ~%v", got, want500)
	}
	// 503: 0.02 - 0.0 = +0.02
	if got := d.StatusDelta["503"]; abs(got-0.02) > 1e-9 {
		t.Errorf("StatusDelta[503] = %v, want ~0.02", got)
	}
}

// ── Diff: combined regression ─────────────────────────────────────────────────

func TestDiff_MultipleRegressions_HighestWins(t *testing.T) {
	// Both latency and error rate regress.
	base := makeSnap(ep("GET:/api", 10, 20, 100, 200, 0.01))
	curr := makeSnap(ep("GET:/api", 12, 25, 130, 300, 0.10))

	result := Diff(base, curr, defaultOpts())
	if result.Endpoints[0].Verdict != VerdictRegression {
		t.Errorf("expected REGRESSION when multiple regressions, got %v", result.Endpoints[0].Verdict)
	}
}

// ── calcVerdict unit tests ────────────────────────────────────────────────────

func TestCalcVerdict_Pass_WhenNoThresholdBreached(t *testing.T) {
	d := EndpointDiff{
		LatencyDelta:   LatencyDelta{P99PctChange: 5},
		ErrorRateDelta: 0.01,
		PayloadDelta:   PayloadSizeDelta{AvgPctChange: 10},
	}
	if v := calcVerdict(d, defaultOpts()); v != VerdictPass {
		t.Errorf("expected PASS, got %v", v)
	}
}

func TestCalcVerdict_Regression_WhenP99Exceeds(t *testing.T) {
	d := EndpointDiff{LatencyDelta: LatencyDelta{P99PctChange: 25}}
	if v := calcVerdict(d, defaultOpts()); v != VerdictRegression {
		t.Errorf("expected REGRESSION, got %v", v)
	}
}

func TestCalcVerdict_WarnNotDowngradedToPass(t *testing.T) {
	// Payload growth → WARN, but not downgraded.
	d := EndpointDiff{
		PayloadDelta: PayloadSizeDelta{AvgPctChange: 60},
	}
	if v := calcVerdict(d, defaultOpts()); v != VerdictWarn {
		t.Errorf("expected WARN, got %v", v)
	}
}

// ── diffStatusDist ────────────────────────────────────────────────────────────

func TestDiffStatusDist_EmptyMaps(t *testing.T) {
	delta := diffStatusDist(nil, nil)
	if len(delta) != 0 {
		t.Errorf("expected empty delta for nil maps, got %v", delta)
	}
}

func TestDiffStatusDist_NewCodeInCurrent(t *testing.T) {
	base := map[string]float64{"200": 1.0}
	curr := map[string]float64{"200": 0.95, "429": 0.05}
	delta := diffStatusDist(base, curr)
	if abs(delta["429"]-0.05) > 1e-9 {
		t.Errorf("delta[429] = %v, want 0.05", delta["429"])
	}
	if abs(delta["200"]-(-0.05)) > 1e-9 {
		t.Errorf("delta[200] = %v, want -0.05", delta["200"])
	}
}

// abs is a helper for float comparison
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
