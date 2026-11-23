package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

func fixtureEndpoint(id string, p99, errRate float64, fieldType string, extra bool) snap.EndpointSnap {
	e := snap.EndpointSnap{
		ID: id, RequestCount: 100, ErrorRate: errRate,
		StatusDist:  map[string]float64{"200": 1 - errRate, "500": errRate},
		Latency:     snap.LatencyStats{P50: p99 / 4, P95: p99 / 2, P99: p99, Max: p99 * 2},
		PayloadSize: snap.PayloadSizeStats{Avg: 1000, P95: 1500, Max: 2000},
		Schema: &snap.SchemaSnapshot{SampleCount: 20, Fields: map[string]snap.FieldSchema{
			"id": {Type: fieldType, Presence: 1, Stability: "STABLE"},
		}},
	}
	if extra {
		e.Schema.Fields["added"] = snap.FieldSchema{Type: "string", Presence: 1, Stability: "STABLE"}
	}
	return e
}

func writeFixture(t *testing.T, dir, tag string, at time.Time, eps ...snap.EndpointSnap) string {
	t.Helper()
	path := filepath.Join(dir, snap.FileName(tag, at))
	meta := snap.SnapMeta{Tag: tag, StartTime: at, EndTime: at.Add(time.Minute), PeakRPS: 10, TotalRequests: 100}
	if err := snap.Write(&snap.Snapshot{Version: 1, Meta: meta, Endpoints: eps}, path); err != nil {
		t.Fatal(err)
	}
	return path
}

type violationKey struct{ Endpoint, Verdict, Message string }

func sortedKeys(vs []snap.Violation) []violationKey {
	var out []violationKey
	for _, v := range vs {
		out = append(out, violationKey{v.EndpointID, string(v.Verdict), v.Message})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Endpoint != out[j].Endpoint {
			return out[i].Endpoint < out[j].Endpoint
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// snap_assert must reach the same verdicts as `gg snap assert --reporter json`
// on the same files and thresholds.
func TestMCPSnapAssert_MatchesCLIReport(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	base := writeFixture(t, dir, "base", at,
		fixtureEndpoint("GET:/slow", 100, 0, "number", false),
		fixtureEndpoint("GET:/typed", 50, 0, "number", false),
		fixtureEndpoint("GET:/errs", 60, 0.01, "number", false),
		fixtureEndpoint("GET:/gone", 30, 0, "number", false),
		fixtureEndpoint("GET:/same", 20, 0, "number", false),
	)
	curr := writeFixture(t, dir, "head", at.Add(time.Hour),
		fixtureEndpoint("GET:/slow", 135, 0, "number", false),
		fixtureEndpoint("GET:/typed", 50, 0, "string", true),
		fixtureEndpoint("GET:/errs", 61, 0.2, "number", false),
		fixtureEndpoint("GET:/new", 30, 0, "number", false),
		fixtureEndpoint("GET:/same", 20, 0, "number", false),
	)

	cases := []struct {
		name string
		cli  []string
		args map[string]any
	}{
		{"defaults", nil, map[string]any{}},
		{"strict", []string{"--latency-regression", "30", "--fail-on-warn", "--deny-removed-fields"},
			map[string]any{"latency_p99_regression_pct": 30, "fail_on_warn": true, "deny_removed_fields": true}},
		{"lenient", []string{"--latency-regression", "50", "--error-rate-delta", "0.5"},
			map[string]any{"latency_p99_regression_pct": 50, "error_rate_delta": 0.5}},
	}

	c := startMCPChildEnv(t, []string{"GG_SNAP_DIR=" + dir})
	c.handshake()
	for i, tc := range cases {
		cmd := exec.Command(os.Args[0], append([]string{"snap", "assert", "--baseline", base, "--current", curr, "--reporter", "json"}, tc.cli...)...)
		cmd.Env = append(os.Environ(), "GG_TEST_RUN_AS_GG=1")
		raw, _ := cmd.Output() // exit status 1 means "did not pass"
		var want snap.AssertResult
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatalf("%s: CLI output is not JSON: %v\n%s", tc.name, err, raw)
		}

		tc.args["baseline"], tc.args["current"], tc.args["max_violations"] = base, "head", 100
		out, isErr, text := c.tool(10+i, "snap_assert", tc.args)
		if isErr {
			t.Fatalf("%s: %s", tc.name, text)
		}
		var got struct {
			Passed     bool             `json:"passed"`
			Violations []snap.Violation `json:"violations"`
		}
		b, _ := json.Marshal(out)
		_ = json.Unmarshal(b, &got)

		if got.Passed != want.Passed || !reflect.DeepEqual(sortedKeys(got.Violations), sortedKeys(want.Violations)) {
			t.Errorf("%s: tool says passed=%v %v\nCLI says passed=%v %v", tc.name, got.Passed, sortedKeys(got.Violations), want.Passed, sortedKeys(want.Violations))
		}
		if len(want.Violations) == 0 && tc.name != "lenient" {
			t.Errorf("%s: fixture should produce violations", tc.name)
		}
	}

	// The diff tool agrees with the library on every endpoint verdict.
	out, isErr, text := c.tool(30, "snap_diff", map[string]any{"baseline": base, "current": "head"})
	if isErr {
		t.Fatal(text)
	}
	bs, _ := snap.Read(base)
	cs, _ := snap.Read(curr)
	want := map[string]string{}
	for _, e := range snap.Diff(bs, cs, snap.DefaultDiffOptions()).Endpoints {
		want[e.ID] = string(e.Verdict)
	}
	eps, _ := out["endpoints"].([]any)
	if len(eps) != len(want) {
		t.Fatalf("endpoints = %d, want %d", len(eps), len(want))
	}
	for _, e := range eps {
		m := e.(map[string]any)
		if want[m["id"].(string)] != m["verdict"] {
			t.Errorf("%v: tool %v, library %s", m["id"], m["verdict"], want[m["id"].(string)])
		}
	}
}
