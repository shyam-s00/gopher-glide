package snap

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update", false, "rewrite the report golden files")

func reportFixture() *Snapshot {
	start := time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)
	params, _ := json.Marshal(map[string]any{"mode": "find-ceiling", "max_p99_ms": 200, "max_error_rate": 0.01, "step_pct": 25, "start_rps": 20})
	return &Snapshot{
		Version: 1,
		Meta: SnapMeta{
			Tag: "checkout-ceiling", StartTime: start, EndTime: start.Add(90 * time.Second), PeakRPS: 400, TotalRequests: 5200,
			Marks: []Mark{
				{Label: "Autopilot: ceiling breached at 110 RPS (p99=351ms > 200ms; top failing: POST /checkout 503); backing off to 95 RPS", ElapsedS: 61.4, Source: "autopilot"},
				{Label: "cache flushed | retrying", ElapsedS: 33, Source: "agent"},
				{Label: "looking at the dashboard", ElapsedS: 12.5},
			},
			BiasEvents: []BiasEvent{{Amount: 5, Cumulative: 5, ElapsedS: 8, Source: "user"}},
			HoldEvents: []HoldEvent{{RPS: 40, ElapsedS: 20, Source: "agent"}, {Released: true, ElapsedS: 28, Source: "agent"}},
			Autopilot: &AutopilotMeta{
				Mode: "find-ceiling", Outcome: "ceiling_found", CeilingRPS: 95, Params: params,
				Events: []AutopilotEvent{
					{State: "stepping", HoldRPS: 30, Reason: "starting at 30 RPS", ElapsedS: 40},
					{State: "stepping", HoldRPS: 80, Reason: "healthy at 65 RPS; stepping to 80", ElapsedS: 50},
					{State: "stepping", HoldRPS: 110, Reason: "healthy at 95 RPS; stepping to 110", ElapsedS: 58},
					{State: "breached", HoldRPS: 110, Reason: "ceiling breached at 110 RPS", ElapsedS: 61.4},
					{State: "confirming", HoldRPS: 95, Reason: "confirming 95 RPS for 15s", ElapsedS: 61.4},
					{State: "done", HoldRPS: 95, CeilingRPS: 95, Outcome: "ceiling_found", Reason: "ceiling confirmed at 95 RPS", ElapsedS: 77},
				},
			},
		},
		Endpoints: []EndpointSnap{
			{ID: "POST:/checkout", RequestCount: 1800, ErrorRate: 0.0444, StatusDist: map[string]float64{"200": 0.9556, "503": 0.0444},
				Latency: LatencyStats{P50: 41.2, P95: 180.55, P99: 351, Max: 920}},
			{ID: "GET:/users/{{id}}", RequestCount: 3400, ErrorRate: 0, StatusDist: map[string]float64{"200": 1},
				Latency: LatencyStats{P50: 8.4, P95: 19, P99: 33.7, Max: 120}},
		},
	}
}

func preAutopilotFixture() *Snapshot {
	s := reportFixture()
	s.Meta.Autopilot, s.Meta.HoldEvents, s.Meta.BiasEvents = nil, nil, nil
	s.Meta.Marks = []Mark{{Label: "deploy finished", ElapsedS: 30}}
	s.Meta.Tag, s.Meta.ProfileName, s.Meta.ProfileScale = "baseline", "load", 1.5
	return s
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs from the golden file; run with -update to accept.\n--- got ---\n%s", name, got)
	}
}

func TestReport_Golden(t *testing.T) {
	cases := []struct {
		name string
		snap *Snapshot
		fmt  ReportFormat
		file string
	}{
		{"autopilot markdown", reportFixture(), ReportMarkdown, "report_autopilot.md"},
		{"autopilot html", reportFixture(), ReportHTML, "report_autopilot.html"},
		{"pre-autopilot markdown", preAutopilotFixture(), ReportMarkdown, "report_plain.md"},
		{"pre-autopilot html", preAutopilotFixture(), ReportHTML, "report_plain.html"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := RenderReport(c.snap, "", c.fmt)
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, c.file, got)
		})
	}
}

func TestReport_PreAutopilotSnapshotHasNoAutopilotSection(t *testing.T) {
	for _, f := range []ReportFormat{ReportMarkdown, ReportHTML} {
		got, err := RenderReport(preAutopilotFixture(), "", f)
		if err != nil {
			t.Fatal(err)
		}
		text := stripStyle(string(got))
		if strings.Contains(strings.ToLower(text), "autopilot") || strings.Contains(text, "<svg") {
			t.Errorf("%s: a snapshot without an autopilot or any rate events must not mention one or draw a chart:\n%s", f, text)
		}
		if !strings.Contains(text, "deploy finished") || !strings.Contains(text, "GET:/users/{{id}}") {
			t.Errorf("%s: the run's own data is missing:\n%s", f, text)
		}
	}
}

var styleBlock = regexp.MustCompile(`(?s)<style>.*?</style>`)

// stripStyle drops the stylesheet, whose class names mention every source.
func stripStyle(s string) string { return styleBlock.ReplaceAllString(s, "") }

var externalRef = regexp.MustCompile(`(?i)(<script|<link|<img|<iframe|<object|@import|url\(|\bsrc=|\bhref=|https?://|//[a-z0-9.-]+\.[a-z]{2,})`)

func TestReport_HTMLIsSelfContained(t *testing.T) {
	got, err := RenderReport(reportFixture(), "", ReportHTML)
	if err != nil {
		t.Fatal(err)
	}
	if loc := externalRef.FindIndex(got); loc != nil {
		t.Errorf("html must make no external requests and run no scripts; found %q", got[loc[0]:loc[1]])
	}
	if !strings.Contains(string(got), "<svg") || !strings.Contains(string(got), `class="rate"`) {
		t.Error("the rate chart is missing")
	}
}

func TestReport_EscapesSnapshotText(t *testing.T) {
	s := reportFixture()
	s.Endpoints[0].ID = `GET:/<script>alert(1)</script>|x`
	s.Meta.Marks[0].Label = "<img src=x onerror=alert(1)> | pipe\nnewline"
	html, err := RenderReport(s, "run <b>", ReportHTML)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "<script>alert") || strings.Contains(string(html), "<img src=x") || strings.Contains(string(html), "run <b>") {
		t.Errorf("html did not escape snapshot text:\n%s", html)
	}
	md, err := RenderReport(s, "", ReportMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(md), "\n") {
		if strings.Contains(line, "pipe") && (strings.Count(line, "|")-strings.Count(line, `\|`) != 4) {
			t.Errorf("an unescaped pipe or newline broke the table row: %q", line)
		}
	}
}

func TestReport_EmptyAndOddSnapshotsRender(t *testing.T) {
	cases := map[string]*Snapshot{
		"empty":           {Version: 1},
		"no end time":     {Version: 1, Meta: SnapMeta{StartTime: time.Now()}, Endpoints: []EndpointSnap{{ID: "GET:/a"}}},
		"hold, no marks":  {Version: 1, Meta: SnapMeta{HoldEvents: []HoldEvent{{RPS: 10, ElapsedS: 5}}}},
		"only a release":  {Version: 1, Meta: SnapMeta{HoldEvents: []HoldEvent{{Released: true, ElapsedS: 5}}}},
		"autopilot no ev": {Version: 1, Meta: SnapMeta{Autopilot: &AutopilotMeta{Mode: "slo-guard", Outcome: "completed"}}},
		"bad params json": {Version: 1, Meta: SnapMeta{Autopilot: &AutopilotMeta{Mode: "slo-guard", Outcome: "stopped", Params: json.RawMessage(`{`)}}},
		"unknown outcome": {Version: 1, Meta: SnapMeta{Autopilot: &AutopilotMeta{Mode: "x", Outcome: "future_outcome"}}},
	}
	for name, s := range cases {
		for _, f := range []ReportFormat{ReportMarkdown, ReportHTML} {
			got, err := RenderReport(s, "", f)
			if err != nil || len(got) == 0 {
				t.Errorf("%s/%s: err=%v len=%d", name, f, err, len(got))
			}
		}
	}
}

func TestReport_FailingEndpointsRankedAndCapped(t *testing.T) {
	s := &Snapshot{Version: 1}
	for i, n := range []int64{10, 50, 30, 20, 40, 60, 5} {
		s.Endpoints = append(s.Endpoints, EndpointSnap{
			ID: "GET:/e" + string(rune('a'+i)), RequestCount: 100, ErrorRate: float64(n) / 100,
			StatusDist: map[string]float64{"200": 1 - float64(n)/100, "500": float64(n) / 100},
		})
	}
	s.Endpoints = append(s.Endpoints, EndpointSnap{ID: "GET:/clean", RequestCount: 100, StatusDist: map[string]float64{"200": 1}})
	m := buildReport(s, "")
	if len(m.Failing) != 5 || m.Failing[0].ID != "GET:/ef" || m.Failing[4].ID != "GET:/ed" {
		t.Fatalf("failing = %+v", m.Failing)
	}
	for _, f := range m.Failing {
		if f.ID == "GET:/clean" || f.Cause != "HTTP 500" {
			t.Errorf("unexpected failing row %+v", f)
		}
	}
	if m.Requests != 800 {
		t.Errorf("requests = %d", m.Requests)
	}
}

func TestReport_TimelineSortedAndSourcesKept(t *testing.T) {
	m := buildReport(reportFixture(), "")
	for i := 1; i < len(m.Timeline); i++ {
		if m.Timeline[i].ElapsedS < m.Timeline[i-1].ElapsedS {
			t.Fatalf("timeline out of order at %d: %+v", i, m.Timeline)
		}
	}
	got := map[string]int{}
	for _, e := range m.Timeline {
		got[e.Kind+"/"+e.Source]++
	}
	for _, want := range []string{"mark/autopilot", "mark/agent", "mark/", "bias/user", "hold/agent", "autopilot/autopilot"} {
		if got[want] == 0 {
			t.Errorf("timeline lacks %q: %v", want, got)
		}
	}
}

func TestParseReportFormat(t *testing.T) {
	for in, want := range map[string]ReportFormat{"md": ReportMarkdown, "MD": ReportMarkdown, "markdown": ReportMarkdown, " html ": ReportHTML} {
		if got, err := ParseReportFormat(in); err != nil || got != want {
			t.Errorf("%q = %v, %v", in, got, err)
		}
	}
	if _, err := ParseReportFormat("pdf"); err == nil {
		t.Error("pdf accepted")
	}
	if ReportHTML.Ext() != ".html" || ReportMarkdown.Ext() != ".md" {
		t.Error("extensions wrong")
	}
}
