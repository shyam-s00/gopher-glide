package snap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ReportFormat selects the mission report's output.
type ReportFormat string

const (
	// ReportMarkdown is GitHub-flavoured Markdown (tables, no images), sized for a PR comment.
	ReportMarkdown ReportFormat = "md"
	// ReportHTML is one self-contained file: inline CSS and an inline SVG chart, no scripts or requests.
	ReportHTML ReportFormat = "html"
)

// ParseReportFormat accepts "md" or "html" (case-insensitive).
func ParseReportFormat(s string) (ReportFormat, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "md", "markdown":
		return ReportMarkdown, nil
	case "html":
		return ReportHTML, nil
	}
	return "", fmt.Errorf("report format must be md or html, got %q", s)
}

// ReportExt is the file extension for a format, with its dot.
func (f ReportFormat) Ext() string {
	if f == ReportHTML {
		return ".html"
	}
	return ".md"
}

// RenderReport renders one snapshot as a mission report. title names the run
// (a tag or file name); it falls back to the snapshot's own tag.
func RenderReport(s *Snapshot, title string, f ReportFormat) ([]byte, error) {
	m := buildReport(s, title)
	switch f {
	case ReportMarkdown:
		return []byte(m.markdown()), nil
	case ReportHTML:
		return m.html()
	}
	return nil, fmt.Errorf("unknown report format %q", f)
}

type reportModel struct {
	Title     string
	Meta      SnapMeta
	DurationS float64

	Requests  int64
	ErrorRate float64
	Worst     LatencyStats // highest value of each percentile across endpoints

	Endpoints []reportEndpoint
	Failing   []reportEndpoint
	Timeline  []reportEvent
	Autopilot *reportAutopilot
	Chart     *reportChart
}

type reportEndpoint struct {
	ID       string
	Requests int64
	Failures int64
	ErrorPct float64
	Latency  LatencyStats
	Status   string // top status codes, "200 97%, 503 3%"
	Cause    string // dominant failing status, for the failing table
}

type reportEvent struct {
	ElapsedS float64
	Source   string
	Kind     string // autopilot | mark | bias | hold
	Text     string
	MarkNo   int // 1-based position among the run's marks in time order; 0 for other events
}

type reportAutopilot struct {
	Mode, Outcome, Summary string
	CeilingRPS             int
	Params                 [][2]string
	Events                 []AutopilotEvent
}

func buildReport(s *Snapshot, title string) *reportModel {
	m := &reportModel{Title: title, Meta: s.Meta}
	if m.Title == "" {
		m.Title = s.Meta.Tag
	}
	if m.Title == "" {
		m.Title = "run"
	}
	if !s.Meta.EndTime.IsZero() && s.Meta.EndTime.After(s.Meta.StartTime) {
		m.DurationS = s.Meta.EndTime.Sub(s.Meta.StartTime).Seconds()
	}

	var failures int64
	for _, e := range s.Endpoints {
		row := reportEndpoint{ID: e.ID, Requests: e.RequestCount, Latency: e.Latency, ErrorPct: e.ErrorRate * 100,
			Failures: int64(math.Round(e.ErrorRate * float64(e.RequestCount))), Status: topStatus(e.StatusDist, 3)}
		row.Cause = failingCause(e.StatusDist)
		m.Endpoints = append(m.Endpoints, row)
		m.Requests += e.RequestCount
		failures += row.Failures
		m.Worst.P50, m.Worst.P95 = math.Max(m.Worst.P50, e.Latency.P50), math.Max(m.Worst.P95, e.Latency.P95)
		m.Worst.P99, m.Worst.Max = math.Max(m.Worst.P99, e.Latency.P99), math.Max(m.Worst.Max, e.Latency.Max)
		if row.Failures > 0 {
			m.Failing = append(m.Failing, row)
		}
	}
	if m.Requests > 0 {
		m.ErrorRate = float64(failures) / float64(m.Requests)
	}
	sort.SliceStable(m.Endpoints, func(i, j int) bool { return m.Endpoints[i].ID < m.Endpoints[j].ID })
	sort.SliceStable(m.Failing, func(i, j int) bool {
		if m.Failing[i].Failures != m.Failing[j].Failures {
			return m.Failing[i].Failures > m.Failing[j].Failures
		}
		return m.Failing[i].ID < m.Failing[j].ID
	})
	if len(m.Failing) > 5 {
		m.Failing = m.Failing[:5]
	}

	m.Autopilot = buildAutopilot(s.Meta.Autopilot)
	m.Timeline = buildTimeline(s.Meta)
	m.Chart = buildChart(s.Meta, m.DurationS)
	return m
}

func buildAutopilot(a *AutopilotMeta) *reportAutopilot {
	if a == nil {
		return nil
	}
	v := &reportAutopilot{Mode: a.Mode, Outcome: a.Outcome, CeilingRPS: a.CeilingRPS, Events: a.Events}
	switch a.Outcome {
	case "ceiling_found":
		v.Summary = fmt.Sprintf("Highest sustainable rate: %d RPS.", a.CeilingRPS)
	case "ceiling_not_reached":
		v.Summary = fmt.Sprintf("No ceiling found: the target stayed healthy up to %d RPS, where the search hit its rate or time limit.", a.CeilingRPS)
	case "unstable":
		v.Summary = "No rate could be confirmed healthy; the target was unstable."
	case "completed":
		v.Summary = "The guard watched the whole run."
	case "stopped":
		v.Summary = "The autopilot was stopped before it reached a result."
	}
	var params map[string]any
	if len(a.Params) > 0 && json.Unmarshal(a.Params, &params) == nil {
		keys := make([]string, 0, len(params))
		for k := range params {
			if k != "mode" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			v.Params = append(v.Params, [2]string{k, fmt.Sprint(params[k])})
		}
	}
	return v
}

func buildTimeline(meta SnapMeta) []reportEvent {
	var ev []reportEvent
	if a := meta.Autopilot; a != nil {
		for _, e := range a.Events {
			text := e.State
			if e.HoldRPS > 0 {
				text += fmt.Sprintf(" (hold %d RPS)", e.HoldRPS)
			}
			if e.Reason != "" {
				text += ": " + e.Reason
			}
			ev = append(ev, reportEvent{ElapsedS: e.ElapsedS, Source: "autopilot", Kind: "autopilot", Text: text})
		}
	}
	for _, h := range meta.HoldEvents {
		text := fmt.Sprintf("hold %d RPS", h.RPS)
		if h.Released {
			text = "release hold"
		}
		ev = append(ev, reportEvent{ElapsedS: h.ElapsedS, Source: h.Source, Kind: "hold", Text: text})
	}
	for _, b := range meta.BiasEvents {
		ev = append(ev, reportEvent{ElapsedS: b.ElapsedS, Source: b.Source, Kind: "bias",
			Text: fmt.Sprintf("bias %+d RPS (total %+d)", b.Amount, b.Cumulative)})
	}
	for _, mk := range meta.Marks {
		ev = append(ev, reportEvent{ElapsedS: mk.ElapsedS, Source: mk.Source, Kind: "mark", Text: mk.Label})
	}
	sort.SliceStable(ev, func(i, j int) bool { return ev[i].ElapsedS < ev[j].ElapsedS })
	n := 0
	for i := range ev {
		if ev[i].Kind == "mark" {
			n++
			ev[i].MarkNo = n
		}
	}
	return ev
}

func topStatus(dist map[string]float64, n int) string {
	type kv struct {
		code  string
		share float64
	}
	var all []kv
	for c, s := range dist {
		all = append(all, kv{c, s})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].share != all[j].share {
			return all[i].share > all[j].share
		}
		return all[i].code < all[j].code
	})
	if len(all) > n {
		all = all[:n]
	}
	parts := make([]string, len(all))
	for i, e := range all {
		parts[i] = fmt.Sprintf("%s %s", e.code, pct(e.share*100))
	}
	return strings.Join(parts, ", ")
}

// failingCause names the most common failing status, or "transport" when no
// HTTP error status dominates.
func failingCause(dist map[string]float64) string {
	best, share := "", 0.0
	for c, s := range dist {
		code, err := strconv.Atoi(c)
		if err != nil || code < 400 {
			continue
		}
		if s > share || (s == share && c < best) {
			best, share = c, s
		}
	}
	if best == "" {
		return "transport or extraction failure"
	}
	return "HTTP " + best
}

func pct(v float64) string {
	if v >= 10 || v == 0 {
		return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) + "%"
	}
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) + "%"
}

func ms(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }

func source(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func secs(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', 1, 64) + "s" }

func (m *reportModel) runLine() string {
	var parts []string
	if !m.Meta.StartTime.IsZero() {
		parts = append(parts, m.Meta.StartTime.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if m.DurationS > 0 {
		parts = append(parts, (time.Duration(m.DurationS * float64(time.Second))).Round(time.Second).String())
	}
	if m.Meta.ProfileName != "" {
		p := "profile " + m.Meta.ProfileName
		if m.Meta.ProfileScale > 0 && m.Meta.ProfileScale != 1 {
			p += fmt.Sprintf(" (x%.2g)", m.Meta.ProfileScale)
		}
		parts = append(parts, p)
	}
	if m.Meta.PeakRPS > 0 {
		parts = append(parts, fmt.Sprintf("plan peak %d RPS", m.Meta.PeakRPS))
	}
	return strings.Join(parts, " · ")
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func (m *reportModel) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# gg run report: %s\n\n", m.Title)
	if line := m.runLine(); line != "" {
		fmt.Fprintf(&b, "%s\n\n", line)
	}

	b.WriteString("## Summary\n\n| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Requests | %d |\n| Error rate | %s |\n", m.Requests, pct(m.ErrorRate*100))
	fmt.Fprintf(&b, "| Latency, slowest endpoint (ms) | p50 %s · p95 %s · p99 %s · max %s |\n\n",
		ms(m.Worst.P50), ms(m.Worst.P95), ms(m.Worst.P99), ms(m.Worst.Max))

	if a := m.Autopilot; a != nil {
		b.WriteString("## Autopilot\n\n")
		fmt.Fprintf(&b, "- **Mode:** %s\n- **Outcome:** %s\n", a.Mode, a.Outcome)
		if a.CeilingRPS > 0 {
			fmt.Fprintf(&b, "- **Ceiling:** %d RPS\n", a.CeilingRPS)
		}
		if a.Summary != "" {
			fmt.Fprintf(&b, "\n%s\n", a.Summary)
		}
		if len(a.Params) > 0 {
			b.WriteString("\n| Setting | Value |\n|---|---|\n")
			for _, p := range a.Params {
				fmt.Fprintf(&b, "| %s | %s |\n", p[0], cell(p[1]))
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("## Timeline\n\n")
	if len(m.Timeline) == 0 {
		b.WriteString("No marks, steering or autopilot events were recorded.\n\n")
	} else {
		b.WriteString("| Time | Source | Event |\n|---|---|---|\n")
		for _, e := range m.Timeline {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", secs(e.ElapsedS), source(e.Source), cell(e.Text))
		}
		b.WriteString("\n")
	}

	if len(m.Failing) > 0 {
		b.WriteString("## Top failing endpoints\n\n| Endpoint | Failures | Error rate | Mostly |\n|---|---|---|---|\n")
		for _, e := range m.Failing {
			fmt.Fprintf(&b, "| `%s` | %d | %s | %s |\n", cell(e.ID), e.Failures, pct(e.ErrorPct), e.Cause)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Endpoints\n\n| Endpoint | Requests | Errors | p50 | p95 | p99 | Status |\n|---|---|---|---|---|---|---|\n")
	for _, e := range m.Endpoints {
		fmt.Fprintf(&b, "| `%s` | %d | %s | %s | %s | %s | %s |\n", cell(e.ID), e.Requests, pct(e.ErrorPct),
			ms(e.Latency.P50), ms(e.Latency.P95), ms(e.Latency.P99), cell(e.Status))
	}
	b.WriteString("\n_Generated by gg from the run's snapshot._\n")
	return b.String()
}

// ---- HTML ----

type chartPoint struct {
	T   float64
	RPS int // 0 means the hold was released
}

type reportChart struct {
	SVG template.HTML
}

const (
	chartW, chartH                 = 720, 260
	chartL, chartR, chartT, chartB = 52, 16, 14, 34
	chartDefaultSpan               = 60.0
)

// buildChart draws the rate the run was pinned to over time, with the marks.
// Snapshots keep no per-second traffic series, so a run with no hold or
// autopilot events has nothing to plot.
func buildChart(meta SnapMeta, duration float64) *reportChart {
	var pts []chartPoint
	if a := meta.Autopilot; a != nil {
		for _, e := range a.Events {
			switch {
			case e.HoldRPS > 0:
				pts = append(pts, chartPoint{e.ElapsedS, e.HoldRPS})
			case e.State == "watching" || e.State == "done":
				pts = append(pts, chartPoint{e.ElapsedS, 0})
			}
		}
	}
	for _, h := range meta.HoldEvents {
		if h.Released {
			pts = append(pts, chartPoint{h.ElapsedS, 0})
		} else {
			pts = append(pts, chartPoint{h.ElapsedS, h.RPS})
		}
	}
	if len(pts) == 0 {
		return nil
	}
	sort.SliceStable(pts, func(i, j int) bool { return pts[i].T < pts[j].T })
	pts = dedupePoints(pts)

	span, top := duration, 0
	for _, p := range pts {
		span, top = math.Max(span, p.T), max(top, p.RPS)
	}
	for _, mk := range meta.Marks {
		span = math.Max(span, mk.ElapsedS)
	}
	if span <= 0 {
		span = chartDefaultSpan
	}
	if top == 0 {
		return nil
	}
	yMax := niceCeil(float64(top) * 1.1)

	x := func(t float64) float64 { return chartL + t/span*float64(chartW-chartL-chartR) }
	y := func(v float64) float64 { return float64(chartH-chartB) - v/yMax*float64(chartH-chartB-chartT) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" role="img" aria-label="Pinned request rate over time, with marks">`, chartW, chartH)
	for i := 0; i <= 4; i++ {
		v := yMax * float64(i) / 4
		fmt.Fprintf(&b, `<line class="grid" x1="%d" y1="%.1f" x2="%d" y2="%.1f"/><text class="tick" x="%d" y="%.1f" text-anchor="end">%s</text>`,
			chartL, y(v), chartW-chartR, y(v), chartL-6, y(v)+4, strconv.FormatFloat(v, 'f', -1, 64))
	}
	for i := 0; i <= 5; i++ {
		t := span * float64(i) / 5
		fmt.Fprintf(&b, `<text class="tick" x="%.1f" y="%d" text-anchor="middle">%s</text>`, x(t), chartH-chartB+16, html.EscapeString(secs(t)))
	}
	fmt.Fprintf(&b, `<text class="tick" x="%d" y="%d">RPS held</text>`, chartL, chartT-3+0)

	// Step path per held stretch; a release (0) breaks the line.
	var path strings.Builder
	for i, p := range pts {
		if p.RPS == 0 {
			flushPath(&b, &path)
			continue
		}
		end := span
		if i+1 < len(pts) {
			end = pts[i+1].T
		}
		if path.Len() == 0 {
			fmt.Fprintf(&path, "M%.1f %.1f", x(p.T), y(float64(p.RPS)))
		} else {
			fmt.Fprintf(&path, " L%.1f %.1f", x(p.T), y(float64(p.RPS)))
		}
		fmt.Fprintf(&path, " L%.1f %.1f", x(end), y(float64(p.RPS)))
		if i+1 < len(pts) && pts[i+1].RPS == 0 {
			flushPath(&b, &path)
		}
	}
	flushPath(&b, &path)

	marks := append([]Mark(nil), meta.Marks...)
	sort.SliceStable(marks, func(i, j int) bool { return marks[i].ElapsedS < marks[j].ElapsedS })
	for i, mk := range marks {
		fmt.Fprintf(&b, `<g class="mark %s"><line x1="%.1f" y1="%d" x2="%.1f" y2="%d"/><text x="%.1f" y="%d" text-anchor="middle">%d</text></g>`,
			markClass(mk.Source), x(mk.ElapsedS), chartT, x(mk.ElapsedS), chartH-chartB, x(mk.ElapsedS), chartT+10, i+1)
	}
	b.WriteString(`</svg>`)
	return &reportChart{SVG: template.HTML(b.String())} //nolint:gosec // built from numbers and escaped text only
}

// dedupePoints drops a point that repeats the previous one's rate.
func dedupePoints(pts []chartPoint) []chartPoint {
	out := pts[:0]
	for _, p := range pts {
		if n := len(out); n > 0 && out[n-1].RPS == p.RPS {
			continue
		}
		out = append(out, p)
	}
	return out
}

func flushPath(out, path *strings.Builder) {
	if path.Len() > 0 {
		fmt.Fprintf(out, `<path class="rate" d="%s"/>`, path.String())
		path.Reset()
	}
}

func markClass(src string) string {
	switch src {
	case "autopilot", "agent", "user":
		return "src-" + src
	}
	return "src-none"
}

func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	exp := math.Pow(10, math.Floor(math.Log10(v)))
	for _, f := range []float64{1, 2, 2.5, 5, 10} {
		if v <= f*exp {
			return f * exp
		}
	}
	return 10 * exp
}

func (m *reportModel) html() ([]byte, error) {
	var buf bytes.Buffer
	if err := reportTemplate.Execute(&buf, m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"pct": pct, "rate": func(v float64) string { return pct(v * 100) }, "ms": ms, "secs": secs, "source": source,
	"srcClass": markClass, "inc": func(i int) int { return i + 1 },
}).Parse(reportHTML))

const reportHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>gg run report: {{.Title}}</title>
<style>
:root{--bg:#fff;--fg:#1c2025;--muted:#5d6672;--line:#d9dee5;--card:#f5f7fa;--accent:#2563eb;--autopilot:#7c3aed;--agent:#0e7490;--user:#b45309}
@media (prefers-color-scheme:dark){:root{--bg:#14171c;--fg:#e6e9ee;--muted:#9aa4b2;--line:#2c333d;--card:#1b2027;--accent:#6ea0ff;--autopilot:#b196ff;--agent:#58c4dc;--user:#f0b35a}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:960px;margin:0 auto;padding:24px 16px 48px}
h1{font-size:1.5rem;margin:0 0 4px}h2{font-size:1.1rem;margin:32px 0 10px}
.sub{color:var(--muted);margin:0 0 20px}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:10px}
.card{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:10px 12px}
.card b{display:block;font-size:1.25rem}.card span{color:var(--muted);font-size:.8rem}
table{width:100%;border-collapse:collapse;font-size:.9rem}
th,td{text-align:left;padding:6px 8px;border-bottom:1px solid var(--line);vertical-align:top}
th{color:var(--muted);font-weight:600}td.n,th.n{text-align:right;font-variant-numeric:tabular-nums}
code{font:13px ui-monospace,SFMono-Regular,Menlo,monospace}
.badge{display:inline-block;border:1px solid currentColor;border-radius:999px;padding:0 8px;font-size:.75rem}
.src-autopilot{color:var(--autopilot)}.src-agent{color:var(--agent)}.src-user{color:var(--user)}.src-none{color:var(--muted)}
.scroll{overflow-x:auto}
svg{width:100%;height:auto;max-width:720px;display:block}
svg .grid{stroke:var(--line);stroke-width:1}svg .tick{fill:var(--muted);font-size:11px}
svg .rate{fill:none;stroke:var(--accent);stroke-width:2.5}
svg .mark line{stroke:currentColor;stroke-width:1.5;stroke-dasharray:4 3}svg .mark text{fill:currentColor;font-size:11px;font-weight:700}
.note{color:var(--muted);font-size:.85rem}
</style>
</head>
<body>
<main>
<h1>gg run report: {{.Title}}</h1>
<p class="sub">{{if not .Meta.StartTime.IsZero}}{{.Meta.StartTime.UTC.Format "2006-01-02 15:04 UTC"}}{{end}}{{if gt .DurationS 0.0}} · {{secs .DurationS}}{{end}}{{if .Meta.ProfileName}} · profile {{.Meta.ProfileName}}{{end}}{{if gt .Meta.PeakRPS 0}} · plan peak {{.Meta.PeakRPS}} RPS{{end}}</p>

<div class="cards">
<div class="card"><b>{{.Requests}}</b><span>requests</span></div>
<div class="card"><b>{{rate .ErrorRate}}</b><span>error rate</span></div>
<div class="card"><b>{{ms .Worst.P50}} ms</b><span>p50, slowest endpoint</span></div>
<div class="card"><b>{{ms .Worst.P95}} ms</b><span>p95, slowest endpoint</span></div>
<div class="card"><b>{{ms .Worst.P99}} ms</b><span>p99, slowest endpoint</span></div>
</div>
{{with .Autopilot}}
<h2>Autopilot</h2>
<p><span class="badge src-autopilot">{{.Mode}}</span> outcome <b>{{.Outcome}}</b>{{if gt .CeilingRPS 0}} · ceiling <b>{{.CeilingRPS}} RPS</b>{{end}}</p>
{{if .Summary}}<p>{{.Summary}}</p>{{end}}
{{if .Params}}<div class="scroll"><table><thead><tr><th>Setting</th><th>Value</th></tr></thead><tbody>
{{range .Params}}<tr><td>{{index . 0}}</td><td>{{index . 1}}</td></tr>
{{end}}</tbody></table></div>{{end}}
{{end}}
<h2>Timeline</h2>
{{with .Chart}}<div class="scroll">{{.SVG}}</div>
<p class="note">Pinned request rate over the run (autopilot and manual holds). Numbered lines are the marks (#) in the table below; a gap means the rate followed the plan. A snapshot keeps no per-second traffic series, so this shows the rate commands, not measured throughput.</p>{{end}}
{{if .Timeline}}<div class="scroll"><table><thead><tr><th class="n">Time</th><th>Source</th><th>Event</th></tr></thead><tbody>
{{range .Timeline}}<tr><td class="n">{{secs .ElapsedS}}</td><td><span class="badge {{srcClass .Source}}">{{source .Source}}</span></td><td>{{if .MarkNo}}<b>#{{.MarkNo}}</b> {{end}}{{.Text}}</td></tr>
{{end}}</tbody></table></div>
{{else}}<p class="note">No marks, steering or autopilot events were recorded.</p>{{end}}
{{if .Failing}}
<h2>Top failing endpoints</h2>
<div class="scroll"><table><thead><tr><th>Endpoint</th><th class="n">Failures</th><th class="n">Error rate</th><th>Mostly</th></tr></thead><tbody>
{{range .Failing}}<tr><td><code>{{.ID}}</code></td><td class="n">{{.Failures}}</td><td class="n">{{pct .ErrorPct}}</td><td>{{.Cause}}</td></tr>
{{end}}</tbody></table></div>
{{end}}
<h2>Endpoints</h2>
<div class="scroll"><table><thead><tr><th>Endpoint</th><th class="n">Requests</th><th class="n">Errors</th><th class="n">p50</th><th class="n">p95</th><th class="n">p99</th><th>Status</th></tr></thead><tbody>
{{range .Endpoints}}<tr><td><code>{{.ID}}</code></td><td class="n">{{.Requests}}</td><td class="n">{{pct .ErrorPct}}</td><td class="n">{{ms .Latency.P50}}</td><td class="n">{{ms .Latency.P95}}</td><td class="n">{{ms .Latency.P99}}</td><td>{{.Status}}</td></tr>
{{end}}</tbody></table></div>
<p class="note">Latencies in ms. Generated by gg from the run's snapshot; it holds no headers or response bodies.</p>
</main>
</body>
</html>
`
