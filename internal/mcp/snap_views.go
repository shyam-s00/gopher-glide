package mcp

import (
	"fmt"
	"sort"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// SnapHeader identifies a snapshot in a tool result.
type SnapHeader struct {
	Path          string  `json:"path"`
	Tag           string  `json:"tag,omitempty"`
	Profile       string  `json:"profile,omitempty"`
	StartedAt     string  `json:"started_at,omitempty"`
	DurationS     float64 `json:"duration_s,omitempty"`
	PeakRPS       int     `json:"peak_rps"`
	TotalRequests int64   `json:"total_requests"`
	Endpoints     int     `json:"endpoints"`
}

func headerOf(path string, s *snap.Snapshot) SnapHeader {
	m := s.Meta
	h := SnapHeader{
		Path: path, Tag: m.Tag, Profile: m.ProfileName, PeakRPS: m.PeakRPS,
		TotalRequests: m.TotalRequests, Endpoints: len(s.Endpoints),
	}
	if !m.StartTime.IsZero() {
		h.StartedAt = m.StartTime.UTC().Format(time.RFC3339)
		if m.EndTime.After(m.StartTime) {
			h.DurationS = r2(m.EndTime.Sub(m.StartTime).Seconds())
		}
	}
	return h
}

// Steering describes manual changes made during a run, which can explain a difference between two runs.
type Steering struct {
	Snapshot   string `json:"snapshot"`
	Marks      int    `json:"marks"`
	BiasEvents int    `json:"bias_events"`
	FinalBias  int    `json:"final_bias"`
}

func steeringOf(which string, s *snap.Snapshot) *Steering {
	m := s.Meta
	if len(m.Marks) == 0 && len(m.BiasEvents) == 0 && m.FinalBias == 0 {
		return nil
	}
	return &Steering{Snapshot: which, Marks: len(m.Marks), BiasEvents: len(m.BiasEvents), FinalBias: m.FinalBias}
}

func steeringAdvisory(a, b *Steering) (string, []Steering) {
	var list []Steering
	for _, s := range []*Steering{a, b} {
		if s != nil {
			list = append(list, *s)
		}
	}
	if len(list) == 0 {
		return "", nil
	}
	return "Traffic was steered by hand during at least one of these runs (rate changes or marks), so differences may come from that rather than from the system under test.", list
}

// MarkView is a mark saved in a snapshot.
type MarkView struct {
	Label    string  `json:"label"`
	ElapsedS float64 `json:"elapsed_s"`
}

// CodeShare is the share of responses with one status code.
type CodeShare struct {
	Code  string  `json:"code"`
	Share float64 `json:"share"`
}

// SchemaCounts summarises an endpoint's inferred response schema.
type SchemaCounts struct {
	Fields   int `json:"fields"`
	Stable   int `json:"stable"`
	Volatile int `json:"volatile"`
	Rare     int `json:"rare"`
}

// EndpointView is the trimmed profile of one endpoint.
type EndpointView struct {
	ID         string        `json:"id"`
	Requests   int64         `json:"requests"`
	ErrorRate  float64       `json:"error_rate"`
	P50Ms      float64       `json:"p50_ms"`
	P95Ms      float64       `json:"p95_ms"`
	P99Ms      float64       `json:"p99_ms"`
	MaxMs      float64       `json:"max_ms"`
	AvgBytes   float64       `json:"avg_payload_bytes"`
	StatusMix  []CodeShare   `json:"status_mix,omitempty"`
	Schema     *SchemaCounts `json:"schema,omitempty"`
	SchemaFrom int           `json:"schema_samples,omitempty"`
}

func endpointView(e snap.EndpointSnap) EndpointView {
	v := EndpointView{
		ID: e.ID, Requests: e.RequestCount, ErrorRate: r4(e.ErrorRate),
		P50Ms: r2(e.Latency.P50), P95Ms: r2(e.Latency.P95), P99Ms: r2(e.Latency.P99), MaxMs: r2(e.Latency.Max),
		AvgBytes: r2(e.PayloadSize.Avg),
	}
	for code, share := range e.StatusDist {
		v.StatusMix = append(v.StatusMix, CodeShare{Code: code, Share: r4(share)})
	}
	sort.Slice(v.StatusMix, func(i, j int) bool {
		if v.StatusMix[i].Share != v.StatusMix[j].Share {
			return v.StatusMix[i].Share > v.StatusMix[j].Share
		}
		return v.StatusMix[i].Code < v.StatusMix[j].Code
	})
	if len(v.StatusMix) > 4 {
		v.StatusMix = v.StatusMix[:4]
	}
	if e.Schema != nil {
		c := &SchemaCounts{Fields: len(e.Schema.Fields)}
		for _, f := range e.Schema.Fields {
			switch f.Stability {
			case "STABLE":
				c.Stable++
			case "VOLATILE":
				c.Volatile++
			case "RARE":
				c.Rare++
			}
		}
		v.Schema, v.SchemaFrom = c, e.Schema.SampleCount
	}
	return v
}

// Trimmed endpoint list, busiest first.
func endpointViews(eps []snap.EndpointSnap, limit int) (views []EndpointView, omitted int) {
	sorted := append([]snap.EndpointSnap(nil), eps...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].RequestCount != sorted[j].RequestCount {
			return sorted[i].RequestCount > sorted[j].RequestCount
		}
		return sorted[i].ID < sorted[j].ID
	})
	if len(sorted) > limit {
		omitted, sorted = len(sorted)-limit, sorted[:limit]
	}
	for _, e := range sorted {
		views = append(views, endpointView(e))
	}
	return views, omitted
}

func marksView(s *snap.Snapshot) (marks []MarkView, omitted int) {
	all := s.Meta.Marks
	if len(all) > maxMarksShown {
		omitted, all = len(all)-maxMarksShown, all[:maxMarksShown]
	}
	for _, m := range all {
		marks = append(marks, MarkView{Label: clipText(m.Label, 80), ElapsedS: r2(m.ElapsedS)})
	}
	return marks, omitted
}

func clipText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// Diff views.

// FieldChangeView is a schema change, trimmed.
type FieldChangeView struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// Compared holds the deltas of an endpoint present in both snapshots.
type Compared struct {
	P50Pct         float64           `json:"p50_pct"`
	P95Pct         float64           `json:"p95_pct"`
	P99Pct         float64           `json:"p99_pct"`
	ErrorRateDelta float64           `json:"error_rate_delta"`
	PayloadAvgPct  float64           `json:"payload_avg_pct"`
	StatusShift    []CodeShare       `json:"status_shift,omitempty"`
	SchemaChanges  []FieldChangeView `json:"schema_changes,omitempty"`
	SchemaOmitted  int               `json:"schema_changes_omitted,omitempty"`
}

// EndpointDeltaView is one endpoint of a diff.
type EndpointDeltaView struct {
	ID       string    `json:"id"`
	Verdict  string    `json:"verdict"`
	Presence string    `json:"presence,omitempty"` // "removed" or "new"
	Compared *Compared `json:"compared,omitempty"`
}

func verdictRank(v snap.DiffVerdict) int {
	switch v {
	case snap.VerdictRegression:
		return 0
	case snap.VerdictWarn:
		return 1
	}
	return 2
}

func fieldDetail(fc snap.FieldChange) string {
	switch fc.Kind {
	case snap.FieldTypeChanged:
		return fmt.Sprintf("%s -> %s", fc.BaseType, fc.CurrType)
	case snap.FieldStabilityChanged:
		return fmt.Sprintf("%s -> %s", fc.BaseStability, fc.CurrStability)
	case snap.ArrayBloat:
		return fmt.Sprintf("avg length %.1f -> %.1f", fc.BaseArrayLength, fc.CurrArrayLength)
	}
	return ""
}

func deltaView(e snap.EndpointDiff) EndpointDeltaView {
	v := EndpointDeltaView{ID: e.ID, Verdict: string(e.Verdict)}
	switch {
	case e.BaselineOnly:
		v.Presence = "removed"
		return v
	case e.CurrentOnly:
		v.Presence = "new"
		return v
	}
	c := &Compared{
		P50Pct: r2(e.LatencyDelta.P50PctChange), P95Pct: r2(e.LatencyDelta.P95PctChange), P99Pct: r2(e.LatencyDelta.P99PctChange),
		ErrorRateDelta: r4(e.ErrorRateDelta), PayloadAvgPct: r2(e.PayloadDelta.AvgPctChange),
	}
	for code, d := range e.StatusDelta {
		if r4(d) != 0 {
			c.StatusShift = append(c.StatusShift, CodeShare{Code: code, Share: r4(d)})
		}
	}
	sort.Slice(c.StatusShift, func(i, j int) bool {
		a, b := abs(c.StatusShift[i].Share), abs(c.StatusShift[j].Share)
		if a != b {
			return a > b
		}
		return c.StatusShift[i].Code < c.StatusShift[j].Code
	})
	if len(c.StatusShift) > 3 {
		c.StatusShift = c.StatusShift[:3]
	}
	changes := append([]snap.FieldChange(nil), e.SchemaChanges...)
	sort.SliceStable(changes, func(i, j int) bool { return fieldRank(changes[i].Kind) < fieldRank(changes[j].Kind) })
	if len(changes) > maxSchemaShown {
		c.SchemaOmitted, changes = len(changes)-maxSchemaShown, changes[:maxSchemaShown]
	}
	for _, fc := range changes {
		c.SchemaChanges = append(c.SchemaChanges, FieldChangeView{Path: fc.Path, Kind: string(fc.Kind), Detail: fieldDetail(fc)})
	}
	v.Compared = c
	return v
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// fieldRank puts the breaking kinds of schema change first.
func fieldRank(k snap.FieldChangeKind) int {
	switch k {
	case snap.FieldTypeChanged:
		return 0
	case snap.ArrayBloat:
		return 1
	case snap.FieldRemoved:
		return 2
	}
	return 3
}

// worstFirst orders endpoint diffs by verdict, then by how much slower or more error-prone they got.
func worstFirst(eps []snap.EndpointDiff) []snap.EndpointDiff {
	out := append([]snap.EndpointDiff(nil), eps...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := verdictRank(a.Verdict), verdictRank(b.Verdict); ra != rb {
			return ra < rb
		}
		if a.LatencyDelta.P99PctChange != b.LatencyDelta.P99PctChange {
			return a.LatencyDelta.P99PctChange > b.LatencyDelta.P99PctChange
		}
		if a.ErrorRateDelta != b.ErrorRateDelta {
			return a.ErrorRateDelta > b.ErrorRateDelta
		}
		return a.ID < b.ID
	})
	return out
}

// VerdictCounts tallies endpoints by verdict.
type VerdictCounts struct {
	Pass       int `json:"pass"`
	Warn       int `json:"warn"`
	Regression int `json:"regression"`
}

func countVerdicts(eps []snap.EndpointDiff) (c VerdictCounts) {
	for _, e := range eps {
		switch e.Verdict {
		case snap.VerdictRegression:
			c.Regression++
		case snap.VerdictWarn:
			c.Warn++
		default:
			c.Pass++
		}
	}
	return c
}

func overall(c VerdictCounts) string {
	switch {
	case c.Regression > 0:
		return string(snap.VerdictRegression)
	case c.Warn > 0:
		return string(snap.VerdictWarn)
	}
	return string(snap.VerdictPass)
}
