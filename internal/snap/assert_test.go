package snap

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func assertOpts() AssertOptions { return DefaultAssertOptions() }

func snapWithMeta(tag string, endpoints ...EndpointSnap) *Snapshot {
	s := makeSnap(endpoints...)
	s.Meta.Tag = tag
	s.Meta.StartTime = time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	return s
}

// ── DefaultAssertOptions ──────────────────────────────────────────────────────

func TestDefaultAssertOptions_SensibleDefaults(t *testing.T) {
	opts := DefaultAssertOptions()
	if opts.LatencyP99RegressionPct != 20 {
		t.Errorf("LatencyP99RegressionPct = %v, want 20", opts.LatencyP99RegressionPct)
	}
	if opts.ErrorRateDeltaThreshold != 0.05 {
		t.Errorf("ErrorRateDeltaThreshold = %v, want 0.05", opts.ErrorRateDeltaThreshold)
	}
	if opts.PayloadSizeAvgPctThreshold != 50 {
		t.Errorf("PayloadSizeAvgPctThreshold = %v, want 50", opts.PayloadSizeAvgPctThreshold)
	}
	if opts.FailOnWarn {
		t.Error("FailOnWarn should default to false")
	}
}

// ── Assert: Passed flag ───────────────────────────────────────────────────────

func TestAssert_NoRegressions_Passes(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0.01))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 100, 200, 0.01))

	result := Assert(base, curr, assertOpts())
	if !result.Passed {
		t.Errorf("expected Passed=true, got violations: %+v", result.Violations)
	}
	if len(result.Violations) != 0 {
		t.Errorf("expected 0 violations, got %d", len(result.Violations))
	}
}

func TestAssert_P99Regression_Fails(t *testing.T) {
	// P99: 100 → 135 = 35% increase, threshold is 20%
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0.01))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 135, 200, 0.01))

	result := Assert(base, curr, assertOpts())
	if result.Passed {
		t.Error("expected Passed=false for P99 regression")
	}
	if len(result.Violations) == 0 {
		t.Fatal("expected at least one violation")
	}
	v := result.Violations[0]
	if v.Verdict != VerdictRegression {
		t.Errorf("violation verdict = %v, want REGRESSION", v.Verdict)
	}
	if !strings.Contains(v.Message, "P99 latency") {
		t.Errorf("violation message should mention P99 latency, got %q", v.Message)
	}
	if !strings.Contains(v.Message, "threshold") {
		t.Errorf("violation message should mention threshold, got %q", v.Message)
	}
}

func TestAssert_ErrorRateSpike_Fails(t *testing.T) {
	// error rate: 0 → 0.10 = 10pp, threshold is 5pp
	base := snapWithMeta("v1", ep("POST:/login", 10, 20, 30, 50, 0.0))
	curr := snapWithMeta("v2", ep("POST:/login", 10, 20, 30, 50, 0.10))

	result := Assert(base, curr, assertOpts())
	if result.Passed {
		t.Error("expected Passed=false for error rate spike")
	}
	found := false
	for _, v := range result.Violations {
		if strings.Contains(v.Message, "error rate") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an error rate violation, got %+v", result.Violations)
	}
}

func TestAssert_FieldTypeChanged_Fails(t *testing.T) {
	base := snapWithMeta("v1", epWithSchema("GET:/users", makeSchema(map[string]FieldSchema{
		"count": {Type: "number", Presence: 1.0, Stability: StabilityStable},
	})))
	curr := snapWithMeta("v2", epWithSchema("GET:/users", makeSchema(map[string]FieldSchema{
		"count": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))

	result := Assert(base, curr, assertOpts())
	if result.Passed {
		t.Error("expected Passed=false for field type change")
	}
	found := false
	for _, v := range result.Violations {
		if strings.Contains(v.Message, "type changed") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a type-changed violation, got %+v", result.Violations)
	}
}

func TestAssert_FieldRemoved_WithoutDeny_Passes(t *testing.T) {
	base := snapWithMeta("v1", epWithSchema("GET:/users", makeSchema(map[string]FieldSchema{
		"id":    {Type: "string", Presence: 1.0, Stability: StabilityStable},
		"email": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))
	curr := snapWithMeta("v2", epWithSchema("GET:/users", makeSchema(map[string]FieldSchema{
		"id": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))

	opts := assertOpts()
	opts.DenyRemovedFields = false
	result := Assert(base, curr, opts)
	// Without DenyRemovedFields, removal is only WARN — should not fail unless FailOnWarn.
	if !result.Passed {
		t.Errorf("expected Passed=true (DenyRemovedFields=false, FailOnWarn=false), got violations: %+v", result.Violations)
	}
}

func TestAssert_FieldRemoved_WithDeny_Fails(t *testing.T) {
	base := snapWithMeta("v1", epWithSchema("GET:/users", makeSchema(map[string]FieldSchema{
		"id":    {Type: "string", Presence: 1.0, Stability: StabilityStable},
		"email": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))
	curr := snapWithMeta("v2", epWithSchema("GET:/users", makeSchema(map[string]FieldSchema{
		"id": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))

	opts := assertOpts()
	opts.DenyRemovedFields = true
	result := Assert(base, curr, opts)
	if result.Passed {
		t.Error("expected Passed=false when DenyRemovedFields=true and a field was removed")
	}
	found := false
	for _, v := range result.Violations {
		if strings.Contains(v.Message, "removed") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 'removed' violation message, got %+v", result.Violations)
	}
}

// ── Assert: FailOnWarn ────────────────────────────────────────────────────────

func TestAssert_PayloadGrowth_PassesWithoutFailOnWarn(t *testing.T) {
	// 60% payload growth → WARN, but FailOnWarn is false
	base := snapWithMeta("v1", epWithPayload("GET:/api", 10, 1000, 1500, 3000))
	curr := snapWithMeta("v2", epWithPayload("GET:/api", 10, 1600, 2000, 4000))

	opts := assertOpts()
	opts.FailOnWarn = false
	result := Assert(base, curr, opts)
	if !result.Passed {
		t.Errorf("expected Passed=true without FailOnWarn, got violations: %+v", result.Violations)
	}
}

func TestAssert_PayloadGrowth_FailsWithFailOnWarn(t *testing.T) {
	// 60% payload growth → WARN, FailOnWarn=true → should fail
	base := snapWithMeta("v1", epWithPayload("GET:/api", 10, 1000, 1500, 3000))
	curr := snapWithMeta("v2", epWithPayload("GET:/api", 10, 1600, 2000, 4000))

	opts := assertOpts()
	opts.FailOnWarn = true
	result := Assert(base, curr, opts)
	if result.Passed {
		t.Error("expected Passed=false when FailOnWarn=true and payload grew > threshold")
	}
	if len(result.Violations) == 0 {
		t.Fatal("expected at least one violation")
	}
}

func TestAssert_FieldAdded_FailsWithFailOnWarn(t *testing.T) {
	// FieldAdded → WARN verdict; should fail when FailOnWarn=true
	base := snapWithMeta("v1", epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"id": {Type: "string", Presence: 1.0, Stability: StabilityStable},
	})))
	curr := snapWithMeta("v2", epWithSchema("GET:/api", makeSchema(map[string]FieldSchema{
		"id":    {Type: "string", Presence: 1.0, Stability: StabilityStable},
		"extra": {Type: "string", Presence: 0.5, Stability: StabilityVolatile},
	})))

	opts := assertOpts()
	opts.FailOnWarn = true
	result := Assert(base, curr, opts)
	if result.Passed {
		t.Error("expected Passed=false when FailOnWarn=true and a field was added")
	}
}

// ── Assert: baseline-only / current-only ──────────────────────────────────────

func TestAssert_BaselineOnlyEndpoint_FailsWithFailOnWarn(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/gone", 10, 20, 30, 50, 0))
	curr := snapWithMeta("v2")

	opts := assertOpts()
	opts.FailOnWarn = true
	result := Assert(base, curr, opts)
	if result.Passed {
		t.Error("expected Passed=false for baseline-only endpoint with FailOnWarn=true")
	}
	if len(result.Violations) == 0 {
		t.Fatal("expected violations")
	}
	if !strings.Contains(result.Violations[0].Message, "baseline-only") {
		t.Errorf("expected 'baseline-only' in message, got %q", result.Violations[0].Message)
	}
}

func TestAssert_CurrentOnlyEndpoint_FailsWithFailOnWarn(t *testing.T) {
	base := snapWithMeta("v1")
	curr := snapWithMeta("v2", ep("GET:/new", 10, 20, 30, 50, 0))

	opts := assertOpts()
	opts.FailOnWarn = true
	result := Assert(base, curr, opts)
	if result.Passed {
		t.Error("expected Passed=false for current-only endpoint with FailOnWarn=true")
	}
	if !strings.Contains(result.Violations[0].Message, "current-only") {
		t.Errorf("expected 'current-only' in message, got %q", result.Violations[0].Message)
	}
}

// ── Assert: multiple endpoints & violations ───────────────────────────────────

func TestAssert_MultipleEndpoints_ViolationPerEndpoint(t *testing.T) {
	base := snapWithMeta("v1",
		ep("GET:/a", 10, 20, 100, 200, 0.0),
		ep("GET:/b", 10, 20, 100, 200, 0.0),
	)
	curr := snapWithMeta("v2",
		ep("GET:/a", 10, 20, 135, 200, 0.0), // P99 regression
		ep("GET:/b", 10, 20, 135, 200, 0.0), // P99 regression
	)

	result := Assert(base, curr, assertOpts())
	if result.Passed {
		t.Error("expected Passed=false")
	}
	if len(result.Violations) != 2 {
		t.Errorf("expected 2 violations (one per endpoint), got %d", len(result.Violations))
	}
}

func TestAssert_DiffResultPreserved(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 100, 200, 0))

	result := Assert(base, curr, assertOpts())
	if result.DiffResult.Baseline.Tag != "v1" {
		t.Errorf("DiffResult.Baseline.Tag = %q, want v1", result.DiffResult.Baseline.Tag)
	}
	if result.DiffResult.Current.Tag != "v2" {
		t.Errorf("DiffResult.Current.Tag = %q, want v2", result.DiffResult.Current.Tag)
	}
}

// ── Assert: custom thresholds ─────────────────────────────────────────────────

func TestAssert_CustomLatencyThreshold_Stricter(t *testing.T) {
	// P99: 100 → 108 = 8% increase — passes default (20%) but fails custom (5%)
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 108, 200, 0))

	opts := assertOpts()
	opts.LatencyP99RegressionPct = 5
	result := Assert(base, curr, opts)
	if result.Passed {
		t.Error("expected Passed=false with strict latency threshold (5%)")
	}
}

func TestAssert_CustomLatencyThreshold_Looser(t *testing.T) {
	// P99: 100 → 130 = 30% — fails default (20%) but passes loose (40%)
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 130, 200, 0))

	opts := assertOpts()
	opts.LatencyP99RegressionPct = 40
	result := Assert(base, curr, opts)
	if !result.Passed {
		t.Errorf("expected Passed=true with loose threshold (40%%), got violations: %+v", result.Violations)
	}
}

// ── FormatAssertResult: text ──────────────────────────────────────────────────

func TestFormatAssertResult_Text_Passed(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 100, 200, 0))
	result := Assert(base, curr, assertOpts())

	out, err := FormatAssertResult(result, "text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "PASSED") {
		t.Errorf("expected PASSED in text output, got:\n%s", out)
	}
	if !strings.Contains(out, "v1") || !strings.Contains(out, "v2") {
		t.Errorf("expected tags v1/v2 in text output, got:\n%s", out)
	}
	if !strings.Contains(out, "No threshold violations") {
		t.Errorf("expected 'No threshold violations' in text output, got:\n%s", out)
	}
}

func TestFormatAssertResult_Text_Failed(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 135, 200, 0))
	result := Assert(base, curr, assertOpts())

	out, err := FormatAssertResult(result, "text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("expected FAILED in text output, got:\n%s", out)
	}
	if !strings.Contains(out, "REGRESSION") {
		t.Errorf("expected REGRESSION in text output, got:\n%s", out)
	}
	if !strings.Contains(out, "P99 latency") {
		t.Errorf("expected P99 latency in text output, got:\n%s", out)
	}
}

func TestFormatAssertResult_Text_ShowsVerdictCounts(t *testing.T) {
	base := snapWithMeta("v1",
		ep("GET:/a", 10, 20, 100, 200, 0),
		ep("GET:/b", 10, 20, 100, 200, 0),
	)
	curr := snapWithMeta("v2",
		ep("GET:/a", 10, 20, 135, 200, 0), // REGRESSION
		ep("GET:/b", 10, 20, 100, 200, 0), // PASS
	)
	result := Assert(base, curr, assertOpts())

	out, _ := FormatAssertResult(result, "text")
	if !strings.Contains(out, "1 REGRESSION") {
		t.Errorf("expected '1 REGRESSION' in text output, got:\n%s", out)
	}
	if !strings.Contains(out, "1 PASS") {
		t.Errorf("expected '1 PASS' in text output, got:\n%s", out)
	}
}

// ── FormatAssertResult: json ──────────────────────────────────────────────────

func TestFormatAssertResult_JSON_IsValid(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 135, 200, 0))
	result := Assert(base, curr, assertOpts())

	out, err := FormatAssertResult(result, "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded AssertResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
}

func TestFormatAssertResult_JSON_ContainsPassedField(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 100, 200, 0))
	result := Assert(base, curr, assertOpts())

	out, _ := FormatAssertResult(result, "json")
	if !strings.Contains(out, `"passed": true`) {
		t.Errorf("expected passed:true in JSON, got:\n%s", out)
	}
}

func TestFormatAssertResult_JSON_ViolationsArray(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 135, 200, 0))
	result := Assert(base, curr, assertOpts())

	out, _ := FormatAssertResult(result, "json")
	var decoded AssertResult
	_ = json.Unmarshal([]byte(out), &decoded)
	if len(decoded.Violations) == 0 {
		t.Error("expected violations in decoded JSON")
	}
	if decoded.Violations[0].EndpointID != "GET:/api" {
		t.Errorf("EndpointID = %q, want GET:/api", decoded.Violations[0].EndpointID)
	}
}

// ── FormatAssertResult: markdown ──────────────────────────────────────────────

func TestFormatAssertResult_Markdown_Passed(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 100, 200, 0))
	result := Assert(base, curr, assertOpts())

	out, err := FormatAssertResult(result, "md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "PASSED") {
		t.Errorf("expected PASSED in markdown, got:\n%s", out)
	}
	if !strings.Contains(out, "No threshold violations") {
		t.Errorf("expected 'No threshold violations' in markdown, got:\n%s", out)
	}
}

func TestFormatAssertResult_Markdown_FailedHasViolationsTable(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 135, 200, 0))
	result := Assert(base, curr, assertOpts())

	out, _ := FormatAssertResult(result, "md")
	if !strings.Contains(out, "FAILED") {
		t.Errorf("expected FAILED in markdown, got:\n%s", out)
	}
	if !strings.Contains(out, "Violations") {
		t.Errorf("expected Violations section in markdown, got:\n%s", out)
	}
	if !strings.Contains(out, "| Verdict | Endpoint | Detail |") {
		t.Errorf("expected violations table header in markdown, got:\n%s", out)
	}
	if !strings.Contains(out, "GET:/api") {
		t.Errorf("expected endpoint ID in markdown, got:\n%s", out)
	}
}

func TestFormatAssertResult_Markdown_HasPerEndpointSummary(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 135, 200, 0))
	result := Assert(base, curr, assertOpts())

	out, _ := FormatAssertResult(result, "md")
	if !strings.Contains(out, "Per-Endpoint Summary") {
		t.Errorf("expected Per-Endpoint Summary section in markdown, got:\n%s", out)
	}
	if !strings.Contains(out, "P99 Δ") {
		t.Errorf("expected P99 Δ column in markdown, got:\n%s", out)
	}
}

func TestFormatAssertResult_Markdown_Alias(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 100, 200, 0))
	result := Assert(base, curr, assertOpts())

	outMd, _ := FormatAssertResult(result, "md")
	outMarkdown, _ := FormatAssertResult(result, "markdown")
	if outMd != outMarkdown {
		t.Error("'md' and 'markdown' format aliases should produce identical output")
	}
}

// ── FormatAssertResult: unknown format falls back to text ─────────────────────

func TestFormatAssertResult_UnknownFormat_FallsBackToText(t *testing.T) {
	base := snapWithMeta("v1", ep("GET:/api", 10, 20, 100, 200, 0))
	curr := snapWithMeta("v2", ep("GET:/api", 10, 20, 100, 200, 0))
	result := Assert(base, curr, assertOpts())

	out, err := FormatAssertResult(result, "xml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Falls back to text — should contain the text-format markers.
	if !strings.Contains(out, "Assert Result:") {
		t.Errorf("expected text fallback, got:\n%s", out)
	}
}

// ── internal helpers ──────────────────────────────────────────────────────────

func TestDisplayTag_Empty(t *testing.T) {
	if got := displayTag(""); got != "(untagged)" {
		t.Errorf("displayTag('') = %q, want (untagged)", got)
	}
}

func TestDisplayTag_Run(t *testing.T) {
	if got := displayTag("run"); got != "(untagged)" {
		t.Errorf("displayTag('run') = %q, want (untagged)", got)
	}
}

func TestDisplayTag_Named(t *testing.T) {
	if got := displayTag("v1.2.3"); got != "v1.2.3" {
		t.Errorf("displayTag('v1.2.3') = %q, want v1.2.3", got)
	}
}

func TestTallyVerdicts_AllThree(t *testing.T) {
	diff := DiffResult{
		Endpoints: []EndpointDiff{
			{Verdict: VerdictPass},
			{Verdict: VerdictWarn},
			{Verdict: VerdictWarn},
			{Verdict: VerdictRegression},
		},
	}
	pass, warn, reg := tallyVerdicts(diff)
	if pass != 1 || warn != 2 || reg != 1 {
		t.Errorf("tallyVerdicts = (%d, %d, %d), want (1, 2, 1)", pass, warn, reg)
	}
}

func TestFmtPct_Zero(t *testing.T) {
	if got := fmtPct(0); got != "—" {
		t.Errorf("fmtPct(0) = %q, want —", got)
	}
}

func TestFmtPct_SmallValue_ShowsDash(t *testing.T) {
	if got := fmtPct(0.04); got != "—" {
		t.Errorf("fmtPct(0.04) = %q, want —", got)
	}
}

func TestFmtPct_Positive(t *testing.T) {
	if got := fmtPct(35.0); got != "+35.0%" {
		t.Errorf("fmtPct(35.0) = %q, want +35.0%%", got)
	}
}

func TestFmtPct_Negative(t *testing.T) {
	if got := fmtPct(-12.5); got != "-12.5%" {
		t.Errorf("fmtPct(-12.5) = %q, want -12.5%%", got)
	}
}

func TestFmtPP_Zero(t *testing.T) {
	if got := fmtPP(0); got != "—" {
		t.Errorf("fmtPP(0) = %q, want —", got)
	}
}

func TestFmtPP_Positive(t *testing.T) {
	// 0.10 error rate delta → +10.00 pp
	if got := fmtPP(0.10); got != "+10.00 pp" {
		t.Errorf("fmtPP(0.10) = %q, want +10.00 pp", got)
	}
}

func TestFmtPP_Negative(t *testing.T) {
	if got := fmtPP(-0.05); got != "-5.00 pp" {
		t.Errorf("fmtPP(-0.05) = %q, want -5.00 pp", got)
	}
}

func TestVerdictIcon_AllValues(t *testing.T) {
	cases := []struct {
		v    DiffVerdict
		want string
	}{
		{VerdictPass, "🟢"},
		{VerdictWarn, "🟡"},
		{VerdictRegression, "🔴"},
	}
	for _, c := range cases {
		if got := verdictIcon(c.v); got != c.want {
			t.Errorf("verdictIcon(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}
