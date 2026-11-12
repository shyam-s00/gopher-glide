package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
	"github.com/shyam-s00/gopher-glide/internal/mcp/session/fakegg"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// ep builds an endpoint with latency scaled from p99 and an optional error share.
func ep(id string, requests int64, p99, errRate float64, fields map[string]string) snap.EndpointSnap {
	e := snap.EndpointSnap{
		ID: id, RequestCount: requests, ErrorRate: errRate,
		StatusDist:  map[string]float64{"200": 1 - errRate},
		Latency:     snap.LatencyStats{P50: p99 / 4, P95: p99 / 2, P99: p99, Max: p99 * 2},
		PayloadSize: snap.PayloadSizeStats{Avg: 1000, P95: 1500, Max: 2000},
	}
	if errRate > 0 {
		e.StatusDist["500"] = errRate
	}
	if fields != nil {
		e.Schema = &snap.SchemaSnapshot{SampleCount: 50, Fields: map[string]snap.FieldSchema{}}
		for path, typ := range fields {
			e.Schema.Fields[path] = snap.FieldSchema{Type: typ, Presence: 1, Stability: "STABLE"}
		}
	}
	return e
}

func snapMeta(tag string, mutate func(*snap.SnapMeta)) snap.SnapMeta {
	m := snap.SnapMeta{
		Tag: tag, StartTime: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 11, 7, 10, 1, 0, 0, time.UTC),
		PeakRPS: 50, TotalRequests: 3000,
	}
	if mutate != nil {
		mutate(&m)
	}
	return m
}

// writeSnap stores a snapshot named like the CLI does and returns its path.
func writeSnap(t *testing.T, dir, tag string, at time.Time, meta snap.SnapMeta, eps ...snap.EndpointSnap) string {
	t.Helper()
	path := filepath.Join(dir, snap.FileName(tag, at))
	if err := snap.Write(&snap.Snapshot{Version: 1, Meta: meta, Endpoints: eps}, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func snapDirEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GG_SNAP_DIR", dir)
	return dir
}

func TestResolveSnapRef_Table(t *testing.T) {
	dir := snapDirEnv(t)
	old := writeSnap(t, dir, "v1", time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), snapMeta("v1", nil), ep("GET:/a", 1, 10, 0, nil))
	newer := writeSnap(t, dir, "v1", time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC), snapMeta("v1", nil), ep("GET:/a", 1, 10, 0, nil))
	other := t.TempDir()
	elsewhere := writeSnap(t, other, "x", time.Now(), snapMeta("x", nil), ep("GET:/a", 1, 10, 0, nil))
	notSnap := filepath.Join(other, "notes.txt")
	_ = os.WriteFile(notSnap, []byte("hi"), 0o600)
	_ = old

	cases := []struct {
		name, ref, want, err string
	}{
		{"absolute path", elsewhere, elsewhere, ""},
		{"tag picks the newest", "v1", newer, ""},
		{"tag with spaces around", " v1 ", newer, ""},
		{"numeric id", "2", "", "positional snapshot id"},
		{"numeric id hint names the alternatives", "12", "", "snapshot_path from stop_simulation"},
		{"empty", "  ", "", "reference is empty"},
		{"unknown tag", "nope", "", `no snapshot tagged "nope"`},
		{"bad tag characters", "a b", "", "neither a .snap path nor a valid tag"},
		{"missing file", filepath.Join(other, "gone.snap"), "", "cannot read snapshot"},
		{"wrong extension", notSnap, "", "not a snapshot file"},
		{"directory named like a snapshot", filepath.Join(other, "d.snap"), "", ""},
	}
	if err := os.Mkdir(filepath.Join(other, "d.snap"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases[len(cases)-1].err = "not a regular file"

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveSnapRef(c.ref)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestLoadSnap_CorruptAndForeignFilesNameThePath(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "bad.snap")
	_ = os.WriteFile(corrupt, []byte("{not json"), 0o600)
	foreign := filepath.Join(dir, "other.snap")
	_ = os.WriteFile(foreign, []byte(`{"hello":"world"}`), 0o600)
	huge := filepath.Join(dir, "huge.snap")
	f, _ := os.Create(huge)
	_ = f.Truncate(maxSnapBytes + 1)
	_ = f.Close()

	for path, want := range map[string]string{corrupt: "corrupt or unreadable", foreign: "not a gg snapshot", huge: "larger than"} {
		_, _, err := loadSnap(path)
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want path and %q", path, err, want)
		}
	}
}

func TestEndpointViews_BusiestFirstCappedAndSummarised(t *testing.T) {
	a := ep("GET:/a", 10, 100, 0.1, map[string]string{"id": "number", "name": "string"})
	b := ep("GET:/b", 500, 20, 0, nil)
	c := ep("GET:/c", 500, 30, 0, nil)
	a.StatusDist = map[string]float64{"200": 0.5, "404": 0.2, "500": 0.15, "503": 0.1, "429": 0.05}
	fs := a.Schema.Fields["name"]
	fs.Stability = "VOLATILE"
	a.Schema.Fields["name"] = fs

	views, omitted := endpointViews([]snap.EndpointSnap{a, c, b}, 2)
	if omitted != 1 || len(views) != 2 || views[0].ID != "GET:/b" || views[1].ID != "GET:/c" {
		t.Fatalf("views = %+v omitted %d", views, omitted)
	}
	all, _ := endpointViews([]snap.EndpointSnap{a}, 5)
	v := all[0]
	if len(v.StatusMix) != 4 || v.StatusMix[0].Code != "200" || v.StatusMix[3].Code != "503" {
		t.Errorf("status mix = %+v", v.StatusMix)
	}
	if v.Schema == nil || v.Schema.Fields != 2 || v.Schema.Stable != 1 || v.Schema.Volatile != 1 || v.P99Ms != 100 {
		t.Errorf("view = %+v", v)
	}
}

func snapPair(t *testing.T) (dir, base, curr string) {
	t.Helper()
	dir = snapDirEnv(t)
	base = writeSnap(t, dir, "base", time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), snapMeta("base", nil),
		ep("GET:/fast", 900, 100, 0, map[string]string{"id": "number", "email": "string"}),
		ep("GET:/steady", 800, 50, 0, nil),
		ep("GET:/gone", 100, 40, 0, nil),
		ep("GET:/errs", 700, 60, 0.01, nil),
	)
	curr = writeSnap(t, dir, "head", time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC), snapMeta("head", nil),
		ep("GET:/fast", 900, 160, 0, map[string]string{"id": "string", "email": "string"}), // slower, id changed type
		ep("GET:/steady", 800, 51, 0, nil),
		ep("GET:/new", 100, 40, 0, nil),
		ep("GET:/errs", 700, 62, 0.2, nil), // error spike
	)
	return dir, base, curr
}

func TestSnapDiff_VerdictsMatchTheLibraryAndAreWorstFirst(t *testing.T) {
	_, base, curr := snapPair(t)
	out, err := snapDiff(CompareInput{Baseline: base, Current: "head"})
	if err != nil {
		t.Fatal(err)
	}

	bs, _ := snap.Read(base)
	cs, _ := snap.Read(curr)
	want := map[string]snap.DiffVerdict{}
	for _, e := range snap.Diff(bs, cs, snap.DefaultDiffOptions()).Endpoints {
		want[e.ID] = e.Verdict
	}
	if len(out.Endpoints) != len(want) {
		t.Fatalf("endpoints = %d, want %d", len(out.Endpoints), len(want))
	}
	rank := -1
	for _, e := range out.Endpoints {
		if want[e.ID] != snap.DiffVerdict(e.Verdict) {
			t.Errorf("%s: verdict %s, library says %s", e.ID, e.Verdict, want[e.ID])
		}
		if r := verdictRank(snap.DiffVerdict(e.Verdict)); r < rank {
			t.Errorf("not worst-first: %+v", out.Endpoints)
		} else {
			rank = r
		}
	}
	if out.Overall != "REGRESSION" || out.Counts.Regression != 2 || out.Counts.Warn != 2 || out.Counts.Pass != 0+1 {
		t.Errorf("overall %s counts %+v", out.Overall, out.Counts)
	}
	if out.Endpoints[0].ID != "GET:/fast" {
		t.Errorf("the 60%% slower endpoint with a type change should lead: %+v", out.Endpoints[0])
	}
	fast := out.Endpoints[0].Compared
	if fast == nil || fast.P99Pct != 60 || len(fast.SchemaChanges) != 1 || fast.SchemaChanges[0].Kind != "type_changed" || fast.SchemaChanges[0].Detail != "number -> string" {
		t.Errorf("fast = %+v", fast)
	}
	presence := map[string]string{}
	for _, e := range out.Endpoints {
		presence[e.ID] = e.Presence
	}
	if presence["GET:/gone"] != "removed" || presence["GET:/new"] != "new" {
		t.Errorf("presence = %v", presence)
	}
	if out.Advisory != "" {
		t.Errorf("no steering happened: %q", out.Advisory)
	}
}

func TestSnapDiff_ThresholdsChangeVerdictsAndAreValidated(t *testing.T) {
	_, base, curr := snapPair(t)
	out, err := snapDiff(CompareInput{Baseline: base, Current: curr, Thresholds: Thresholds{LatencyP99Pct: 90, ErrorRateDelta: 0.5}})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range out.Endpoints {
		if e.ID == "GET:/errs" && e.Verdict != "PASS" {
			t.Errorf("a 19-point error spike is under a 50-point threshold: %+v", e)
		}
	}
	if out.Applied.LatencyP99Pct != 90 || out.Applied.PayloadSizePct != 50 {
		t.Errorf("applied = %+v", out.Applied)
	}

	for name, th := range map[string]Thresholds{
		"negative latency": {LatencyP99Pct: -1},
		"error over one":   {ErrorRateDelta: 1.5},
		"negative payload": {PayloadSizePct: -5},
		"negative bloat":   {MaxArrayBloatPct: -1},
	} {
		if _, err := snapDiff(CompareInput{Baseline: base, Current: curr, Thresholds: th}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// Edge 29: nothing panics, and the error says which snapshot and which file.
func TestSnapDiff_MissingAndCorruptInputs(t *testing.T) {
	dir, base, _ := snapPair(t)
	corrupt := filepath.Join(dir, "broken.snap")
	_ = os.WriteFile(corrupt, []byte("\x00\x01 not json"), 0o600)

	for _, c := range []struct {
		in   CompareInput
		want []string
	}{
		{CompareInput{Baseline: base, Current: filepath.Join(dir, "missing.snap")}, []string{"current:", "missing.snap", "cannot read"}},
		{CompareInput{Baseline: corrupt, Current: base}, []string{"baseline:", "broken.snap", "corrupt"}},
		{CompareInput{Baseline: base, Current: "3"}, []string{"current:", "positional snapshot id"}},
		{CompareInput{Baseline: "", Current: base}, []string{"baseline:", "empty"}},
		{CompareInput{Baseline: base, Current: "no-such-tag"}, []string{"current:", "no-such-tag"}},
	} {
		_, err := snapDiff(c.in)
		for _, w := range c.want {
			if err == nil || !strings.Contains(err.Error(), w) {
				t.Errorf("%+v: err = %v, want %q", c.in, err, w)
			}
		}
	}
}

// Edge 30: hundreds of endpoints cannot blow up the answer.
func TestSnapDiff_HugeComparisonStaysSmall(t *testing.T) {
	dir := t.TempDir()
	var baseEps, currEps []snap.EndpointSnap
	for i := 0; i < 400; i++ {
		id := fmt.Sprintf("GET:/service/%03d/resource", i)
		fields := map[string]string{}
		for f := 0; f < 12; f++ {
			fields[fmt.Sprintf("field%d", f)] = "number"
		}
		baseEps = append(baseEps, ep(id, 100, 100, 0, fields))
		changed := map[string]string{}
		for f := range fields {
			changed[f] = "string"
		}
		currEps = append(currEps, ep(id, 100, 100+float64(i), 0.001*float64(i%50), changed))
	}
	base := writeSnap(t, dir, "big-a", time.Now(), snapMeta("a", nil), baseEps...)
	curr := writeSnap(t, dir, "big-b", time.Now().Add(time.Second), snapMeta("b", nil), currEps...)

	out, err := snapDiff(CompareInput{Baseline: base, Current: curr})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Endpoints) != defaultEndpoints || out.EndpointsOmitted != 380 || out.Counts.Regression != 400 {
		t.Fatalf("shown %d omitted %d counts %+v", len(out.Endpoints), out.EndpointsOmitted, out.Counts)
	}
	raw, _ := json.Marshal(out)
	if len(raw) > 16<<10 {
		t.Errorf("diff result is %d bytes, want <= 16 KiB", len(raw))
	}
	for _, e := range out.Endpoints {
		if len(e.Compared.SchemaChanges) != maxSchemaShown || e.Compared.SchemaOmitted != 7 {
			t.Fatalf("schema changes shown %d omitted %d", len(e.Compared.SchemaChanges), e.Compared.SchemaOmitted)
		}
	}
	if out.Endpoints[0].Compared.P99Pct <= out.Endpoints[19].Compared.P99Pct {
		t.Errorf("worst first by slowdown expected within the same verdict")
	}

	over := CompareInput{Baseline: base, Current: curr, MaxEndpoints: 51}
	if _, err := snapDiff(over); err == nil {
		t.Error("max_endpoints above the limit must be refused")
	}
}

func TestSnapDiff_SteeringAdvisory(t *testing.T) {
	dir := snapDirEnv(t)
	plain := writeSnap(t, dir, "plain", time.Now(), snapMeta("plain", nil), ep("GET:/a", 10, 10, 0, nil))
	steered := writeSnap(t, dir, "steered", time.Now().Add(time.Second), snapMeta("steered", func(m *snap.SnapMeta) {
		m.Marks = []snap.Mark{{Label: "cache flush", ElapsedS: 12}}
		m.BiasEvents = []snap.BiasEvent{{Amount: 30, Cumulative: 30, ElapsedS: 10}}
		m.FinalBias = 30
	}), ep("GET:/a", 10, 10, 0, nil))

	out, err := snapDiff(CompareInput{Baseline: plain, Current: steered})
	if err != nil {
		t.Fatal(err)
	}
	if out.Advisory == "" || len(out.Steering) != 1 || out.Steering[0].Snapshot != "current" || out.Steering[0].Marks != 1 || out.Steering[0].FinalBias != 30 {
		t.Fatalf("advisory %q steering %+v", out.Advisory, out.Steering)
	}
	out, _ = snapDiff(CompareInput{Baseline: steered, Current: steered})
	if len(out.Steering) != 2 {
		t.Errorf("both runs steered: %+v", out.Steering)
	}
	out, _ = snapDiff(CompareInput{Baseline: plain, Current: plain})
	if out.Advisory != "" || out.Steering != nil {
		t.Errorf("unsteered runs must not carry an advisory: %+v", out)
	}
}

func TestSnapAssert_FailsOnRegressionAndListsViolations(t *testing.T) {
	_, base, curr := snapPair(t)
	out, err := snapAssert(AssertInput{Baseline: base, Current: curr})
	if err != nil {
		t.Fatal(err)
	}
	if out.Passed || len(out.Violations) == 0 || out.Counts.Regression != 2 {
		t.Fatalf("out = %+v", out)
	}
	for _, v := range out.Violations {
		if v.Verdict != snap.VerdictRegression {
			t.Errorf("warnings must not fail without fail_on_warn: %+v", v)
		}
	}
	msgs := ""
	for _, v := range out.Violations {
		msgs += v.EndpointID + ": " + v.Message + "\n"
	}
	for _, w := range []string{"GET:/fast: P99 latency +60.0%", `field "id" type changed number→string`, "GET:/errs: error rate"} {
		if !strings.Contains(msgs, w) {
			t.Errorf("violations lack %q:\n%s", w, msgs)
		}
	}

	strict, _ := snapAssert(AssertInput{Baseline: base, Current: curr, FailOnWarn: true})
	if len(strict.Violations) <= len(out.Violations) || !strict.Applied.FailOnWarn {
		t.Errorf("fail_on_warn must add the warnings: %d vs %d", len(strict.Violations), len(out.Violations))
	}
	if strict.Violations[len(strict.Violations)-1].Verdict != snap.VerdictWarn {
		t.Errorf("regressions first: %+v", strict.Violations)
	}
}

func TestSnapAssert_PassesWhenNothingMoved(t *testing.T) {
	dir := snapDirEnv(t)
	a := writeSnap(t, dir, "a", time.Now(), snapMeta("a", nil), ep("GET:/a", 10, 10, 0, nil))
	out, err := snapAssert(AssertInput{Baseline: a, Current: a})
	if err != nil || !out.Passed || out.Violations == nil || len(out.Violations) != 0 {
		t.Fatalf("out = %+v err = %v", out, err)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), `"violations":[]`) {
		t.Errorf("an empty list must serialise as [], got %s", raw)
	}
}

func TestSnapAssert_ViolationsAreCappedAndInputChecked(t *testing.T) {
	dir := t.TempDir()
	var baseEps, currEps []snap.EndpointSnap
	for i := 0; i < 150; i++ {
		id := fmt.Sprintf("GET:/r/%03d", i)
		baseEps = append(baseEps, ep(id, 10, 100, 0, nil))
		currEps = append(currEps, ep(id, 10, 500, 0, nil))
	}
	base := writeSnap(t, dir, "a", time.Now(), snapMeta("a", nil), baseEps...)
	curr := writeSnap(t, dir, "b", time.Now().Add(time.Second), snapMeta("b", nil), currEps...)
	out, err := snapAssert(AssertInput{Baseline: base, Current: curr})
	if err != nil || len(out.Violations) != defaultViolations || out.ViolationsOmitted != 130 || out.Passed {
		t.Fatalf("shown %d omitted %d passed %v err %v", len(out.Violations), out.ViolationsOmitted, out.Passed, err)
	}
	if raw, _ := json.Marshal(out); len(raw) > 8<<10 {
		t.Errorf("assert result is %d bytes", len(raw))
	}
	if _, err := snapAssert(AssertInput{Baseline: base, Current: curr, MaxViolations: 101}); err == nil {
		t.Error("max_violations above the limit must be refused")
	}
}

func TestSnapCapture_FileSummaryWithMarks(t *testing.T) {
	dir := snapDirEnv(t)
	path := writeSnap(t, dir, "run", time.Now(), snapMeta("run", func(m *snap.SnapMeta) {
		m.ProfileName = "load"
		m.FinalBias = -5
		for i := 0; i < 12; i++ {
			m.Marks = append(m.Marks, snap.Mark{Label: fmt.Sprintf("m%d", i), ElapsedS: float64(i)})
		}
		m.BiasEvents = []snap.BiasEvent{{Amount: -5, Cumulative: -5}}
	}), ep("GET:/a", 30, 10, 0, nil), ep("GET:/b", 70, 20, 0.2, map[string]string{"x": "number"}))

	for _, ref := range []string{path, "run"} {
		out, err := snapCapture(nil, CaptureInput{Snapshot: ref})
		if err != nil {
			t.Fatal(err)
		}
		h := out.Snapshot
		if out.Source != "file" || h.Path != path || h.Tag != "run" || h.Profile != "load" || h.DurationS != 60 || h.PeakRPS != 50 || h.Endpoints != 2 {
			t.Errorf("header = %+v", h)
		}
		if out.FinalBias != -5 || out.BiasEvents != 1 || len(out.Marks) != maxMarksShown || out.MarksOmitted != 2 {
			t.Errorf("steering = bias %d events %d marks %d omitted %d", out.FinalBias, out.BiasEvents, len(out.Marks), out.MarksOmitted)
		}
		if len(out.Endpoints) != 2 || out.Endpoints[0].ID != "GET:/b" || out.Endpoints[0].Schema == nil {
			t.Errorf("endpoints = %+v", out.Endpoints)
		}
	}
	one, _ := snapCapture(nil, CaptureInput{Snapshot: path, MaxEndpoints: 1})
	if len(one.Endpoints) != 1 || one.EndpointsOmitted != 1 {
		t.Errorf("cap: %+v", one)
	}
}

func TestSnapCapture_InputErrors(t *testing.T) {
	for name, in := range map[string]CaptureInput{
		"neither": {},
		"both":    {SessionID: "sim_x", Snapshot: "t"},
		"bad cap": {Snapshot: "t", MaxEndpoints: 99},
	} {
		if _, err := snapCapture(nil, in); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSnapCapture_FromSession(t *testing.T) {
	dir := snapDirEnv(t)
	path := writeSnap(t, dir, "run", time.Now(), snapMeta("run", nil), ep("GET:/a", 30, 10, 0, nil))
	t.Setenv(fakegg.SnapEnv, path)
	cs, _ := simClient(t, "normal", nil)
	st := startOK(t, cs)

	// Still running: the snapshot does not exist yet.
	res := call(t, cs, "snap_capture", map[string]any{"session_id": st.SessionID}, nil)
	if !res.IsError || !strings.Contains(errText(res), "still") || !strings.Contains(errText(res), "stop_simulation") {
		t.Fatalf("running: isError=%v %q", res.IsError, errText(res))
	}

	call(t, cs, "stop_simulation", map[string]any{"session_id": st.SessionID}, nil)
	var out CaptureOutput
	res = call(t, cs, "snap_capture", map[string]any{"session_id": st.SessionID}, &out)
	if res.IsError || out.Source != "session" || out.SessionID != st.SessionID || out.Snapshot.Path != path || len(out.Endpoints) != 1 {
		t.Fatalf("capture = %+v %s", out, errText(res))
	}

	res = call(t, cs, "snap_capture", map[string]any{"session_id": "sim_nope"}, nil)
	if !res.IsError || !strings.Contains(errText(res), "unknown session_id") {
		t.Errorf("unknown session: %q", errText(res))
	}
}

func TestSessionSnapshot_ExplainsEachMissingCase(t *testing.T) {
	for name, c := range map[string]struct {
		tel  session.Telemetry
		want string
	}{
		"running":     {session.Telemetry{SessionID: "s", State: session.Running}, "still running"},
		"no snapshot": {session.Telemetry{SessionID: "s", State: session.Finished}, "start it with snap: true"},
		"failed":      {session.Telemetry{SessionID: "s", State: session.Failed, Message: "boom"}, "failed before writing a snapshot: boom"},
	} {
		if _, err := sessionSnapshot(c.tel); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if p, err := sessionSnapshot(session.Telemetry{State: session.Stopped, SnapshotPath: "/x.snap"}); err != nil || p != "/x.snap" {
		t.Errorf("p = %q err = %v", p, err)
	}
}

func TestSnapTools_ListedReadOnlyAndDescribed(t *testing.T) {
	cs, _ := simClient(t, "normal", nil)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tl := range list.Tools {
		if !strings.HasPrefix(tl.Name, "snap_") {
			continue
		}
		seen++
		if len(tl.Description) < 150 || tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
			t.Errorf("%s must be read-only and well described: %+v", tl.Name, tl.Annotations)
		}
	}
	if seen != 4 || len(list.Tools) != len(toolNames) {
		t.Errorf("snap tools %d, total tools %d (want %d)", seen, len(list.Tools), len(toolNames))
	}
}

func TestSnapTools_ThroughTheProtocol(t *testing.T) {
	_, base, curr := snapPair(t)
	cs, _ := simClient(t, "normal", nil)

	var diff DiffOutput
	if res := call(t, cs, "snap_diff", map[string]any{"baseline": base, "current": curr, "max_endpoints": 2}, &diff); res.IsError {
		t.Fatal(errText(res))
	}
	if len(diff.Endpoints) != 2 || diff.EndpointsOmitted != 3 || diff.Overall != "REGRESSION" {
		t.Errorf("diff = %+v", diff)
	}
	var as AssertOutput
	if res := call(t, cs, "snap_assert", map[string]any{"baseline": base, "current": "head", "latency_p99_regression_pct": 100}, &as); res.IsError {
		t.Fatal(errText(res))
	}
	if as.Passed || as.Applied.LatencyP99Pct != 100 {
		t.Errorf("assert = %+v", as)
	}
	res := call(t, cs, "snap_diff", map[string]any{"baseline": "1", "current": curr}, nil)
	if !res.IsError || !strings.Contains(errText(res), "positional") {
		t.Errorf("numeric id: %q", errText(res))
	}
}
