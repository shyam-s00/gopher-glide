package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"net/http"
	"net/http/httptest"
	"net/url"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/dryrun"
)

func connectWith(t *testing.T, l Limits) *sdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	s, err := NewServer(Options{Limits: l, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ct, st := sdk.NewInMemoryTransports()
	ss, err := s.SDK.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Wait() })
	return cs
}

// call invokes a tool and decodes its structured output into out.
func call(t *testing.T, cs *sdk.ClientSession, name string, args any, out any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil && !res.IsError {
		raw, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: decode output: %v\n%s", name, err, raw)
		}
	}
	return res
}

func errText(res *sdk.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].(*sdk.TextContent).Text
}

func TestAuthorTools_ListedWithGuidance(t *testing.T) {
	cs := connectWith(t, DefaultLimits())
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*sdk.Tool{}
	for _, tl := range list.Tools {
		got[tl.Name] = tl
		if !slices.Contains(toolNames, tl.Name) {
			t.Errorf("tool %q is not in toolNames", tl.Name)
		}
		if len(tl.Description) < 80 {
			t.Errorf("tool %q needs a real description", tl.Name)
		}
	}
	for _, n := range []string{"validate_http_file", "run_journey_dry_run", "generate_config"} {
		if got[n] == nil {
			t.Errorf("tool %s missing", n)
		}
	}
	dr := got["run_journey_dry_run"]
	if dr.Annotations == nil || dr.Annotations.DestructiveHint == nil || !*dr.Annotations.DestructiveHint {
		t.Errorf("dry run sends real requests and must say so in its annotations: %+v", dr.Annotations)
	}
	if v := got["validate_http_file"]; !v.Annotations.ReadOnlyHint {
		t.Errorf("validate_http_file is read-only")
	}
	raw, _ := json.Marshal(dr.InputSchema)
	for _, f := range []string{`"content"`, `"path"`, `"variables"`, `"journeys"`, `"step_timeout_s"`} {
		if !strings.Contains(string(raw), f) {
			t.Errorf("dry-run input schema lacks %s: %s", f, raw)
		}
	}
}

func TestValidateTool_ContentAndPath(t *testing.T) {
	cs := connectWith(t, DefaultLimits())
	var rep dryrun.Report
	call(t, cs, "validate_http_file", map[string]any{"content": "GET /relative\n"}, &rep)
	if rep.OK || len(rep.Errors) != 1 || rep.Errors[0].Line != 1 {
		t.Fatalf("report = %+v", rep)
	}

	path := filepath.Join(t.TempDir(), "r.http")
	if err := os.WriteFile(path, []byte("GET https://a.example.com/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep = dryrun.Report{}
	call(t, cs, "validate_http_file", map[string]any{"path": path}, &rep)
	if !rep.OK || rep.Requests != 1 {
		t.Fatalf("report from path = %+v", rep)
	}
}

func TestSourceErrors(t *testing.T) {
	cs := connectWith(t, DefaultLimits())
	dir := t.TempDir()
	big := filepath.Join(dir, "big.http")
	_ = os.WriteFile(big, []byte(strings.Repeat("#", dryrun.MaxFileBytes+1)), 0o600)

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"neither", map[string]any{}, "content"},
		{"both", map[string]any{"content": "x", "path": "y"}, "not both"},
		{"missing file", map[string]any{"path": filepath.Join(dir, "nope.http")}, "no such file"},
		{"directory", map[string]any{"path": dir}, "not a regular file"},
		{"too big file", map[string]any{"path": big}, "larger than"},
		{"too big content", map[string]any{"content": strings.Repeat("#", dryrun.MaxFileBytes+1)}, "larger than"},
	}
	for _, tool := range []string{"validate_http_file", "run_journey_dry_run"} {
		for _, c := range cases {
			t.Run(tool+"/"+c.name, func(t *testing.T) {
				res := call(t, cs, tool, c.args, nil)
				if !res.IsError || !strings.Contains(errText(res), c.want) {
					t.Fatalf("isError=%v text=%q, want error containing %q", res.IsError, errText(res), c.want)
				}
			})
		}
	}
}

func targetServer(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write([]byte(`{"token":"SECRET-TOKEN-1","id":"5"}`))
		case "/items/5":
			_, _ = w.Write([]byte(`{"name":"widget"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDryRunTool_EndToEnd(t *testing.T) {
	var hits atomic.Int32
	srv := targetServer(t, &hits)
	cs := connectWith(t, DefaultLimits())

	file := strings.ReplaceAll(`### login
POST BASE/login
# @gg-export token = jsonpath: $.token
# @gg-export id = jsonpath: $.id

### item
GET BASE/items/{{id}}
Authorization: Bearer {{token}}
`, "BASE", srv.URL)

	var out DryRunOutput
	res := call(t, cs, "run_journey_dry_run", map[string]any{"content": file}, &out)
	if res.IsError || out.Result == nil || !out.Result.OK || out.Result.Passed != 2 {
		t.Fatalf("out = %+v text=%q", out, errText(res))
	}
	if strings.Contains(string(mustJSON(out)), "SECRET-TOKEN-1") {
		t.Error("token leaked through the tool output")
	}
	if hits.Load() != 2 {
		t.Errorf("hits = %d", hits.Load())
	}
}

func TestDryRunTool_InvalidFileSendsNothing(t *testing.T) {
	var hits atomic.Int32
	srv := targetServer(t, &hits)
	cs := connectWith(t, DefaultLimits())

	var out DryRunOutput
	call(t, cs, "run_journey_dry_run", map[string]any{
		"content": "GET " + srv.URL + "/login\n\n###\nGET " + srv.URL + "/{{missing}}\n",
	}, &out)
	if out.Validation == nil || out.Result != nil || out.Validation.OK || hits.Load() != 0 {
		t.Fatalf("out=%+v hits=%d", out, hits.Load())
	}
}

func TestDryRunTool_SuppliedVariablesSatisfyValidation(t *testing.T) {
	var hits atomic.Int32
	srv := targetServer(t, &hits)
	cs := connectWith(t, DefaultLimits())
	var out DryRunOutput
	call(t, cs, "run_journey_dry_run", map[string]any{
		"content":   "GET " + srv.URL + "/items/{{id}}\n",
		"variables": map[string]string{"id": "5"},
	}, &out)
	if out.Result == nil || !out.Result.OK || hits.Load() != 1 {
		t.Fatalf("out=%+v hits=%d", out, hits.Load())
	}
}

func TestDryRunTool_JourneySelectionAndLimits(t *testing.T) {
	var hits atomic.Int32
	srv := targetServer(t, &hits)
	cs := connectWith(t, DefaultLimits())
	file := "GET " + srv.URL + "/login\n\n###\nGET " + srv.URL + "/items/5\n"

	var out DryRunOutput
	call(t, cs, "run_journey_dry_run", map[string]any{"content": file, "journeys": []int{2}}, &out)
	if out.Result == nil || len(out.Result.Journeys) != 1 || hits.Load() != 1 {
		t.Fatalf("selection: out=%+v hits=%d", out, hits.Load())
	}

	for _, c := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"journey out of range", map[string]any{"content": file, "journeys": []int{3}}, "does not exist"},
		{"timeout too large", map[string]any{"content": file, "step_timeout_s": 600}, "step_timeout_s"},
		{"preview too large", map[string]any{"content": file, "body_preview_bytes": 999999}, "body_preview_bytes"},
	} {
		res := call(t, cs, "run_journey_dry_run", c.args, nil)
		if !res.IsError || !strings.Contains(errText(res), c.want) {
			t.Errorf("%s: isError=%v text=%q", c.name, res.IsError, errText(res))
		}
	}

	var big strings.Builder
	for i := 0; i < maxDryRunSteps+1; i++ {
		big.WriteString("###\nGET " + srv.URL + "/login\n\n")
	}
	res := call(t, cs, "run_journey_dry_run", map[string]any{"content": big.String()}, nil)
	if !res.IsError || !strings.Contains(errText(res), "limited to") {
		t.Errorf("too many steps: isError=%v text=%q", res.IsError, errText(res))
	}
}

func TestDryRunTool_AllowHostBlocksBeforeAnyTraffic(t *testing.T) {
	var hits atomic.Int32
	srv := targetServer(t, &hits)
	l := DefaultLimits()
	l.AllowHosts = []string{"only.example.com"}
	cs := connectWith(t, l)

	res := call(t, cs, "run_journey_dry_run", map[string]any{"content": "GET " + srv.URL + "/login\n"}, nil)
	if !res.IsError || !strings.Contains(errText(res), "--allow-host") || hits.Load() != 0 {
		t.Fatalf("isError=%v text=%q hits=%d", res.IsError, errText(res), hits.Load())
	}

	host := strings.TrimPrefix(srv.URL, "http://")
	l.AllowHosts = []string{host}
	cs = connectWith(t, l)
	var out DryRunOutput
	call(t, cs, "run_journey_dry_run", map[string]any{"content": "GET " + srv.URL + "/login\n"}, &out)
	if out.Result == nil || !out.Result.OK {
		t.Fatalf("allowed host should run: %+v", out)
	}
}

func TestDryRunTool_ResolvedHostIsCheckedToo(t *testing.T) {
	var hits atomic.Int32
	srv := targetServer(t, &hits)
	l := DefaultLimits()
	l.AllowHosts = []string{strings.TrimPrefix(srv.URL, "http://")}
	cs := connectWith(t, l)

	var out DryRunOutput
	call(t, cs, "run_journey_dry_run", map[string]any{
		"content":   "GET {{base}}/login\n",
		"variables": map[string]string{"base": "http://evil.example.com"},
	}, &out)
	if out.Result == nil || out.Result.OK || hits.Load() != 0 || !strings.Contains(out.Result.Journeys[0].Steps[0].Error, "not allowed") {
		t.Fatalf("out=%+v hits=%d", out, hits.Load())
	}
}

func TestLimits_CheckURL(t *testing.T) {
	parse := func(s string) *url.URL { u, _ := url.Parse(s); return u }
	cases := []struct {
		allow []string
		url   string
		ok    bool
	}{
		{nil, "http://anything.example.com", true},
		{[]string{"a.example.com"}, "https://a.example.com/x", true},
		{[]string{"a.example.com"}, "https://a.example.com:8443/x", true},
		{[]string{"a.example.com:8443"}, "https://a.example.com:8443/x", true},
		{[]string{"a.example.com:8443"}, "https://a.example.com/x", false},
		{[]string{"A.Example.COM"}, "https://a.example.com/", true},
		{[]string{"a.example.com"}, "https://b.example.com/", false},
		{[]string{"a.example.com"}, "https://a.example.com.evil.net/", false},
		{[]string{"a.example.com"}, "https://evil.net/?h=a.example.com", false},
	}
	for _, c := range cases {
		err := Limits{AllowHosts: c.allow}.CheckURL(parse(c.url))
		if (err == nil) != c.ok {
			t.Errorf("allow=%v url=%s: err=%v, want ok=%v", c.allow, c.url, err, c.ok)
		}
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestGenerateConfig_Table(t *testing.T) {
	cases := []struct {
		name    string
		in      ConfigParams
		wantErr string
		check   func(t *testing.T, r ConfigResult, cfg *config.Config)
	}{
		{name: "explicit stages", in: ConfigParams{HTTPFile: "r.http", Jitter: 0.1, TimeScale: 2,
			Stages: []ConfigStage{{Name: "up", Duration: "30s", TargetRPS: 50}, {Duration: "1m", TargetRPS: 50}, {Duration: "0s", TargetRPS: 80}, {Duration: "10s", TargetRPS: 0}}},
			check: func(t *testing.T, r ConfigResult, c *config.Config) {
				if len(c.Stages) != 4 || c.Stages[0].Name != "up" || c.Stages[0].Duration != 30*time.Second || c.Stages[2].Duration != 0 ||
					c.ConfigSection.Jitter != 0.1 || c.ConfigSection.TimeScale != 2 {
					t.Errorf("loaded = %+v", c)
				}
				if !strings.Contains(r.YAML, "duration: 1m\n") {
					t.Errorf("durations should be compact:\n%s", r.YAML)
				}
			}},
		{name: "profile defaults", in: ConfigParams{HTTPFile: "r.http", Profile: "spike"},
			check: func(t *testing.T, r ConfigResult, c *config.Config) {
				if d := c.TotalDuration() - 210*time.Second; c.PeakRPS() != 1000 || d < -50*time.Millisecond || d > 50*time.Millisecond {
					t.Errorf("peak=%d total=%s", c.PeakRPS(), c.TotalDuration())
				}
			}},
		{name: "profile scaled", in: ConfigParams{HTTPFile: "r.http", Profile: "load", PeakRPS: 40, Duration: "2m"},
			check: func(t *testing.T, r ConfigResult, c *config.Config) {
				if c.PeakRPS() != 40 || c.TotalDuration() != 2*time.Minute {
					t.Errorf("peak=%d total=%s", c.PeakRPS(), c.TotalDuration())
				}
			}},
		{name: "profile jitter override carried", in: ConfigParams{HTTPFile: "r.http", Profile: "chaos"},
			check: func(t *testing.T, r ConfigResult, c *config.Config) {
				if c.ConfigSection.Jitter == 0 {
					t.Error("chaos profile bakes in jitter; it should reach the config")
				}
			}},
		{name: "snap block", in: ConfigParams{HTTPFile: "r.http", Profile: "smoke", Snap: &ConfigSnap{SampleRate: 0.1, MaxSamples: 50}},
			check: func(t *testing.T, r ConfigResult, c *config.Config) {
				if c.Snap.SampleRate != 0.1 || c.Snap.MaxSamples != 50 {
					t.Errorf("snap = %+v", c.Snap)
				}
			}},
		{name: "no http file", in: ConfigParams{Profile: "smoke"}, wantErr: "http_file"},
		{name: "neither profile nor stages", in: ConfigParams{HTTPFile: "r.http"}, wantErr: "profile"},
		{name: "both profile and stages", in: ConfigParams{HTTPFile: "r.http", Profile: "smoke", Stages: []ConfigStage{{Duration: "1s", TargetRPS: 1}}}, wantErr: "not both"},
		{name: "unknown profile", in: ConfigParams{HTTPFile: "r.http", Profile: "nope"}, wantErr: "unknown profile"},
		{name: "bad duration", in: ConfigParams{HTTPFile: "r.http", Profile: "smoke", Duration: "soon"}, wantErr: "duration"},
		{name: "bad stage duration", in: ConfigParams{HTTPFile: "r.http", Stages: []ConfigStage{{Duration: "5 minutes", TargetRPS: 1}}}, wantErr: "stages[0].duration"},
		{name: "negative rps", in: ConfigParams{HTTPFile: "r.http", Stages: []ConfigStage{{Duration: "1s", TargetRPS: -1}}}, wantErr: "target_rps"},
		{name: "jitter out of range", in: ConfigParams{HTTPFile: "r.http", Jitter: 2, Stages: []ConfigStage{{Duration: "1s", TargetRPS: 1}}}, wantErr: "jitter"},
		{name: "profile knobs with stages", in: ConfigParams{HTTPFile: "r.http", PeakRPS: 5, Stages: []ConfigStage{{Duration: "1s", TargetRPS: 1}}}, wantErr: "profile"},
		{name: "bad snap rate", in: ConfigParams{HTTPFile: "r.http", Profile: "smoke", Snap: &ConfigSnap{SampleRate: 3}}, wantErr: "sample"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := generateConfig(c.in, DefaultLimits())
			if c.wantErr != "" {
				if r.OK || r.YAML != "" || !strings.Contains(strings.Join(r.Errors, "|"), c.wantErr) {
					t.Fatalf("want error %q, got %+v", c.wantErr, r)
				}
				return
			}
			if !r.OK || len(r.Errors) != 0 {
				t.Fatalf("errors: %v", r.Errors)
			}
			// The output must load with the same loader the CLI uses.
			path := filepath.Join(t.TempDir(), "gg.yaml")
			if err := os.WriteFile(path, []byte(r.YAML), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("generated config does not load: %v\n%s", err, r.YAML)
			}
			c.check(t, r, cfg)
		})
	}
}

func TestGenerateConfig_WarnsWhenOverServerCaps(t *testing.T) {
	r := generateConfig(ConfigParams{HTTPFile: "r.http", Profile: "stress"}, Limits{MaxRPS: 100, MaxDuration: time.Minute, MaxSessions: 1})
	if !r.OK || len(r.Warnings) != 2 {
		t.Fatalf("want ok with two warnings, got %+v", r)
	}
	if r := generateConfig(ConfigParams{HTTPFile: "r.http", Profile: "smoke", PeakRPS: 5, Duration: "30s"}, DefaultLimits()); len(r.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", r.Warnings)
	}
}

func TestGenerateConfigTool_ViaProtocol(t *testing.T) {
	cs := connectWith(t, DefaultLimits())
	var out ConfigResult
	res := call(t, cs, "generate_config", map[string]any{"http_file": "r.http", "profile": "smoke", "peak_rps": 5, "duration": "30s"}, &out)
	if res.IsError || !out.OK || !strings.Contains(out.YAML, "httpFile: r.http") || strings.Contains(out.YAML, "0000000") {
		t.Fatalf("out=%+v", out)
	}
	var bad ConfigResult
	call(t, cs, "generate_config", map[string]any{"http_file": "r.http", "profile": "nope"}, &bad)
	if bad.OK || bad.YAML != "" || len(bad.Errors) == 0 {
		t.Fatalf("invalid input must not yield yaml: %+v", bad)
	}
}
