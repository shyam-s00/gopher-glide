package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

const (
	defaultViolations = 20
	maxViolations     = 100
)

// CaptureInput are the arguments of snap_capture.
type CaptureInput struct {
	SessionID    string `json:"session_id,omitempty" jsonschema:"a simulation started with snap: true; it must have ended. Give either session_id or snapshot"`
	Snapshot     string `json:"snapshot,omitempty" jsonschema:"a snapshot file path (.snap) or a tag; a tag means the newest snapshot with it. Numeric ids are not accepted. Give either session_id or snapshot"`
	MaxEndpoints int    `json:"max_endpoints,omitempty" jsonschema:"endpoints to list, busiest first; default 20, max 50"`
}

// CaptureOutput is the result of snap_capture.
type CaptureOutput struct {
	Source    string     `json:"source"`
	SessionID string     `json:"session_id,omitempty"`
	Snapshot  SnapHeader `json:"snapshot"`
	Note      string     `json:"note,omitempty"`

	FinalBias        int            `json:"final_bias"`
	BiasEvents       int            `json:"bias_events"`
	Marks            []MarkView     `json:"marks,omitempty"`
	MarksOmitted     int            `json:"marks_omitted,omitempty"`
	Endpoints        []EndpointView `json:"endpoints"`
	EndpointsOmitted int            `json:"endpoints_omitted,omitempty"`
}

// Thresholds are the limits a comparison applies. Zero means the default.
type Thresholds struct {
	LatencyP99Pct     float64 `json:"latency_p99_regression_pct,omitempty" jsonschema:"P99 latency increase in percent that counts as a regression; default 20"`
	ErrorRateDelta    float64 `json:"error_rate_delta,omitempty" jsonschema:"error-rate increase (0-1, e.g. 0.05 = 5 points) that counts as a regression; default 0.05"`
	PayloadSizePct    float64 `json:"payload_size_pct,omitempty" jsonschema:"average response size increase in percent that counts as a warning; default 50"`
	DenyRemovedFields bool    `json:"deny_removed_fields,omitempty" jsonschema:"treat a response field that disappeared as a regression instead of a warning"`
	MaxArrayBloatPct  float64 `json:"max_array_bloat_pct,omitempty" jsonschema:"array length growth in percent that counts as a regression; default off"`
}

// CompareInput are the arguments of snap_diff.
type CompareInput struct {
	Baseline     string `json:"baseline" jsonschema:"the earlier, known-good snapshot: a .snap path or a tag"`
	Current      string `json:"current" jsonschema:"the snapshot to judge: a .snap path (for example snapshot_path from stop_simulation) or a tag"`
	MaxEndpoints int    `json:"max_endpoints,omitempty" jsonschema:"endpoints to list, worst first; default 20, max 50"`
	Thresholds
}

// DiffOutput is the result of snap_diff.
type DiffOutput struct {
	Baseline SnapHeader    `json:"baseline"`
	Current  SnapHeader    `json:"current"`
	Overall  string        `json:"overall"`
	Counts   VerdictCounts `json:"counts"`

	Endpoints        []EndpointDeltaView `json:"endpoints"`
	EndpointsOmitted int                 `json:"endpoints_omitted,omitempty"`
	Advisory         string              `json:"advisory,omitempty"`
	Steering         []Steering          `json:"steering,omitempty"`
	Applied          Applied             `json:"thresholds"`
}

// Applied echoes the thresholds that were used.
type Applied struct {
	LatencyP99Pct     float64 `json:"latency_p99_regression_pct"`
	ErrorRateDelta    float64 `json:"error_rate_delta"`
	PayloadSizePct    float64 `json:"payload_size_pct"`
	DenyRemovedFields bool    `json:"deny_removed_fields,omitempty"`
	MaxArrayBloatPct  float64 `json:"max_array_bloat_pct,omitempty"`
	FailOnWarn        bool    `json:"fail_on_warn,omitempty"`
}

// AssertInput are the arguments of snap_assert.
type AssertInput struct {
	Baseline      string `json:"baseline" jsonschema:"the earlier, known-good snapshot: a .snap path or a tag"`
	Current       string `json:"current" jsonschema:"the snapshot to judge: a .snap path or a tag"`
	FailOnWarn    bool   `json:"fail_on_warn,omitempty" jsonschema:"also fail on warnings (new or removed endpoints, added fields, larger payloads), not only regressions"`
	MaxViolations int    `json:"max_violations,omitempty" jsonschema:"violations to list; default 20, max 100"`
	Thresholds
}

// AssertOutput is the result of snap_assert.
type AssertOutput struct {
	Passed            bool             `json:"passed"`
	Counts            VerdictCounts    `json:"counts"`
	Violations        []snap.Violation `json:"violations"`
	ViolationsOmitted int              `json:"violations_omitted,omitempty"`
	Baseline          SnapHeader       `json:"baseline"`
	Current           SnapHeader       `json:"current"`
	Advisory          string           `json:"advisory,omitempty"`
	Steering          []Steering       `json:"steering,omitempty"`
	Applied           Applied          `json:"thresholds"`
}

func registerSnapTools(s *sdk.Server, mgr *session.Manager) {
	ro := &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}

	capAnn := *ro
	capAnn.Title = "Snapshot summary"
	sdk.AddTool(s, &sdk.Tool{
		Name: "snap_capture",
		Description: "Summarise a behavioural snapshot: pass session_id of a simulation that ran with snap: true and has ended, or a snapshot path/tag. " +
			"Returns when and how the run went (profile, peak RPS, total requests, marks you dropped, manual rate changes) and, per endpoint, request count, error rate, p50/p95/p99/max latency, " +
			"top status codes, average payload and response-schema field counts, busiest first and capped. Use it to confirm a new snapshot recorded the endpoints you expected before comparing it.",
		Annotations: &capAnn,
	}, func(_ context.Context, _ *sdk.CallToolRequest, in CaptureInput) (*sdk.CallToolResult, CaptureOutput, error) {
		out, err := snapCapture(mgr, in)
		return nil, out, err
	})

	diffAnn := *ro
	diffAnn.Title = "Compare snapshots"
	sdk.AddTool(s, &sdk.Tool{
		Name: "snap_diff",
		Description: "Compare two snapshots endpoint by endpoint. Returns an overall verdict (PASS, WARN, REGRESSION), counts, and the endpoints worst first with latency change in percent, " +
			"error-rate change, payload change, status-code shifts and response-schema changes; new and removed endpoints are flagged. The list is capped (omitted shows how many were left out). " +
			"If either run was steered by hand (rate changes or marks) the result says so, because that can explain a difference. Use snap_assert for a plain pass/fail.",
		Annotations: &diffAnn,
	}, func(_ context.Context, _ *sdk.CallToolRequest, in CompareInput) (*sdk.CallToolResult, DiffOutput, error) {
		out, err := snapDiff(in)
		return nil, out, err
	})

	asrtAnn := *ro
	asrtAnn.Title = "Assert no regressions"
	sdk.AddTool(s, &sdk.Tool{
		Name: "snap_assert",
		Description: "Pass/fail check of a snapshot against a baseline, with the same thresholds as `gg snap assert`: passed is false when any endpoint regressed " +
			"(P99 latency, error rate, a response field changing type, optionally more) and each violation names the endpoint and the breach. Warnings only fail the check with fail_on_warn. " +
			"Use snap_diff to see the numbers behind a verdict.",
		Annotations: &asrtAnn,
	}, func(_ context.Context, _ *sdk.CallToolRequest, in AssertInput) (*sdk.CallToolResult, AssertOutput, error) {
		out, err := snapAssert(in)
		return nil, out, err
	})
}

func snapCapture(mgr *session.Manager, in CaptureInput) (CaptureOutput, error) {
	limit, err := capEndpoints(in.MaxEndpoints)
	if err != nil {
		return CaptureOutput{}, err
	}
	out := CaptureOutput{Source: "file"}
	ref := in.Snapshot
	switch {
	case in.SessionID != "" && in.Snapshot != "":
		return out, errors.New("give either session_id or snapshot, not both")
	case in.SessionID == "" && in.Snapshot == "":
		return out, errors.New("give session_id (a simulation started with snap: true) or snapshot (a .snap path or tag)")
	case in.SessionID != "":
		sess, err := mgr.Get(in.SessionID)
		if err != nil {
			return out, err
		}
		tel := sess.Telemetry()
		if ref, err = sessionSnapshot(tel); err != nil {
			return out, err
		}
		out.Source, out.SessionID = "session", tel.SessionID
		out.Note = tel.Message
	}

	s, path, err := loadSnap(ref)
	if err != nil {
		return out, err
	}
	out.Snapshot = headerOf(path, s)
	out.FinalBias, out.BiasEvents = s.Meta.FinalBias, len(s.Meta.BiasEvents)
	out.Marks, out.MarksOmitted = marksView(s)
	out.Endpoints, out.EndpointsOmitted = endpointViews(s.Endpoints, limit)
	return out, nil
}

// sessionSnapshot returns the snapshot path of an ended session, or says why there is none.
func sessionSnapshot(t session.Telemetry) (string, error) {
	switch {
	case !t.State.Terminal():
		return "", fmt.Errorf("simulation %s is still %s; the snapshot is written when the run ends, so call stop_simulation or wait for it to finish", t.SessionID, t.State)
	case t.SnapshotPath != "":
		return t.SnapshotPath, nil
	case t.State == session.Failed:
		return "", fmt.Errorf("simulation %s failed before writing a snapshot: %s", t.SessionID, t.Message)
	}
	return "", fmt.Errorf("simulation %s recorded no snapshot; start it with snap: true", t.SessionID)
}

func (t Thresholds) options(failOnWarn bool) (snap.AssertOptions, Applied, error) {
	o := snap.DefaultAssertOptions()
	if t.LatencyP99Pct != 0 {
		o.LatencyP99RegressionPct = t.LatencyP99Pct
	}
	if t.ErrorRateDelta != 0 {
		o.ErrorRateDeltaThreshold = t.ErrorRateDelta
	}
	if t.PayloadSizePct != 0 {
		o.PayloadSizeAvgPctThreshold = t.PayloadSizePct
	}
	o.DenyRemovedFields, o.MaxArrayBloatPct, o.FailOnWarn = t.DenyRemovedFields, t.MaxArrayBloatPct, failOnWarn

	var errs []error
	if o.LatencyP99RegressionPct <= 0 {
		errs = append(errs, fmt.Errorf("latency_p99_regression_pct must be > 0, got %g", o.LatencyP99RegressionPct))
	}
	if o.ErrorRateDeltaThreshold <= 0 || o.ErrorRateDeltaThreshold > 1 {
		errs = append(errs, fmt.Errorf("error_rate_delta must be in (0, 1], got %g", o.ErrorRateDeltaThreshold))
	}
	if o.PayloadSizeAvgPctThreshold <= 0 {
		errs = append(errs, fmt.Errorf("payload_size_pct must be > 0, got %g", o.PayloadSizeAvgPctThreshold))
	}
	if o.MaxArrayBloatPct < 0 {
		errs = append(errs, fmt.Errorf("max_array_bloat_pct must be >= 0, got %g", o.MaxArrayBloatPct))
	}
	return o, Applied{
		LatencyP99Pct: o.LatencyP99RegressionPct, ErrorRateDelta: o.ErrorRateDeltaThreshold, PayloadSizePct: o.PayloadSizeAvgPctThreshold,
		DenyRemovedFields: o.DenyRemovedFields, MaxArrayBloatPct: o.MaxArrayBloatPct, FailOnWarn: failOnWarn,
	}, errors.Join(errs...)
}

// loadPair reads both snapshots, naming which one failed.
func loadPair(baseline, current string) (b, c *snap.Snapshot, bp, cp string, err error) {
	if b, bp, err = loadSnap(baseline); err != nil {
		return nil, nil, "", "", fmt.Errorf("baseline: %w", err)
	}
	if c, cp, err = loadSnap(current); err != nil {
		return nil, nil, "", "", fmt.Errorf("current: %w", err)
	}
	return b, c, bp, cp, nil
}

func snapDiff(in CompareInput) (DiffOutput, error) {
	limit, err := capEndpoints(in.MaxEndpoints)
	if err != nil {
		return DiffOutput{}, err
	}
	opts, applied, err := in.Thresholds.options(false)
	if err != nil {
		return DiffOutput{}, err
	}
	base, curr, bp, cp, err := loadPair(in.Baseline, in.Current)
	if err != nil {
		return DiffOutput{}, err
	}

	res := snap.Diff(base, curr, opts.DiffOptions)
	out := DiffOutput{Baseline: headerOf(bp, base), Current: headerOf(cp, curr), Counts: countVerdicts(res.Endpoints), Applied: applied}
	out.Overall = overall(out.Counts)
	eps := worstFirst(res.Endpoints)
	if len(eps) > limit {
		out.EndpointsOmitted, eps = len(eps)-limit, eps[:limit]
	}
	for _, e := range eps {
		out.Endpoints = append(out.Endpoints, deltaView(e))
	}
	out.Advisory, out.Steering = steeringAdvisory(steeringOf("baseline", base), steeringOf("current", curr))
	return out, nil
}

func snapAssert(in AssertInput) (AssertOutput, error) {
	limit := in.MaxViolations
	switch {
	case limit == 0:
		limit = defaultViolations
	case limit < 0 || limit > maxViolations:
		return AssertOutput{}, fmt.Errorf("max_violations must be between 1 and %d", maxViolations)
	}
	opts, applied, err := in.Thresholds.options(in.FailOnWarn)
	if err != nil {
		return AssertOutput{}, err
	}
	base, curr, bp, cp, err := loadPair(in.Baseline, in.Current)
	if err != nil {
		return AssertOutput{}, err
	}

	res := snap.Assert(base, curr, opts)
	out := AssertOutput{
		Passed: res.Passed, Counts: countVerdicts(res.DiffResult.Endpoints), Violations: []snap.Violation{},
		Baseline: headerOf(bp, base), Current: headerOf(cp, curr), Applied: applied,
	}
	v := append([]snap.Violation(nil), res.Violations...)
	sort.SliceStable(v, func(i, j int) bool { return verdictRank(v[i].Verdict) < verdictRank(v[j].Verdict) })
	if len(v) > limit {
		out.ViolationsOmitted, v = len(v)-limit, v[:limit]
	}
	out.Violations = append(out.Violations, v...)
	out.Advisory, out.Steering = steeringAdvisory(steeringOf("baseline", base), steeringOf("current", curr))
	return out, nil
}
