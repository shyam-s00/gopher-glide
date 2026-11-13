package mcp

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/autopilot"
	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
)

func TestPlanRun_Autopilot(t *testing.T) {
	cases := []struct {
		name    string
		in      StartInput
		limits  func(*Limits)
		wantErr string
		check   func(t *testing.T, run plannedRun)
	}{
		{name: "find-ceiling needs no shape and takes the server caps",
			in: StartInput{HTTPContent: validHTTP, Autopilot: &AutopilotInput{Mode: "find-ceiling"}},
			check: func(t *testing.T, run plannedRun) {
				want := []string{"--autopilot", "find-ceiling", "--max-rps", "1000", "--duration", "5m0s"}
				if !containsRun(run.args, want) {
					t.Errorf("args = %v, want them to include %v", run.args, want)
				}
				if run.peakRPS != 1000 || run.dur != 5*time.Minute+autopilot.FindCeilingMargin {
					t.Errorf("peak=%d dur=%s", run.peakRPS, run.dur)
				}
				if slices.Contains(run.args, "--profile") {
					t.Errorf("find-ceiling must not be given a profile: %v", run.args)
				}
			}},
		{name: "find-ceiling clamps its defaults to a small server",
			in:     StartInput{HTTPContent: validHTTP, Autopilot: &AutopilotInput{Mode: "find-ceiling"}},
			limits: func(l *Limits) { l.MaxRPS, l.MaxDuration = 60, 2*time.Minute },
			check: func(t *testing.T, run plannedRun) {
				if !containsRun(run.args, []string{"--max-rps", "60", "--duration", "1m45s"}) {
					t.Errorf("args = %v", run.args)
				}
			}},
		{name: "thresholds and start rate become flags",
			in: StartInput{HTTPContent: validHTTP, Autopilot: &AutopilotInput{Mode: "find-ceiling", MaxP99Ms: 200, MaxErrorRate: 0.02, StartRPS: 30, MaxRPS: 400, MaxDuration: "90s"}},
			check: func(t *testing.T, run plannedRun) {
				want := []string{"--autopilot", "find-ceiling", "--max-p99", "200ms", "--max-error-rate", "0.02", "--max-rps", "400", "--duration", "1m30s", "--start-rps", "30"}
				if !containsRun(run.args, want) {
					t.Errorf("args = %v, want them to include %v", run.args, want)
				}
			}},
		{name: "slo-guard rides the profile",
			in: StartInput{HTTPContent: validHTTP, Profile: "smoke", Autopilot: &AutopilotInput{Mode: "slo-guard", MaxP99Ms: 150}},
			check: func(t *testing.T, run plannedRun) {
				if !containsRun(run.args, []string{"--profile", "smoke", "--autopilot", "slo-guard", "--max-p99", "150ms"}) {
					t.Errorf("args = %v", run.args)
				}
			}},

		{name: "find-ceiling refuses a profile", in: StartInput{HTTPContent: validHTTP, Profile: "smoke", Autopilot: &AutopilotInput{Mode: "find-ceiling"}}, wantErr: "sets its own schedule"},
		{name: "find-ceiling refuses duration", in: StartInput{HTTPContent: validHTTP, Duration: "1m", Autopilot: &AutopilotInput{Mode: "find-ceiling"}}, wantErr: "sets its own schedule"},
		{name: "slo-guard still needs a shape", in: StartInput{HTTPContent: validHTTP, Autopilot: &AutopilotInput{Mode: "slo-guard"}}, wantErr: "load shape"},
		{name: "slo-guard refuses find-ceiling knobs", in: StartInput{HTTPContent: validHTTP, Profile: "smoke", Autopilot: &AutopilotInput{Mode: "slo-guard", MaxRPS: 5}}, wantErr: "belong to find-ceiling"},
		{name: "unknown mode", in: StartInput{HTTPContent: validHTTP, Profile: "smoke", Autopilot: &AutopilotInput{Mode: "chaos"}}, wantErr: "autopilot.mode"},
		{name: "error rate out of range", in: StartInput{HTTPContent: validHTTP, Profile: "smoke", Autopilot: &AutopilotInput{Mode: "slo-guard", MaxErrorRate: 4}}, wantErr: "max_error_rate"},
		{name: "negative p99", in: StartInput{HTTPContent: validHTTP, Profile: "smoke", Autopilot: &AutopilotInput{Mode: "slo-guard", MaxP99Ms: -1}}, wantErr: "max_p99_ms"},
		{name: "explicit rate above the server cap", in: StartInput{HTTPContent: validHTTP, Autopilot: &AutopilotInput{Mode: "find-ceiling", MaxRPS: 5000}}, wantErr: "--max-rps"},
		{name: "explicit search time above the server cap", in: StartInput{HTTPContent: validHTTP, Autopilot: &AutopilotInput{Mode: "find-ceiling", MaxDuration: "3h"}}, wantErr: "--max-duration"},
		{name: "start rate above max rate", in: StartInput{HTTPContent: validHTTP, Autopilot: &AutopilotInput{Mode: "find-ceiling", StartRPS: 50, MaxRPS: 10}}, wantErr: "start_rps"},
		{name: "bad search time", in: StartInput{HTTPContent: validHTTP, Autopilot: &AutopilotInput{Mode: "find-ceiling", MaxDuration: "soon"}}, wantErr: "max_duration"},
		{name: "too short to search", in: StartInput{HTTPContent: validHTTP, Autopilot: &AutopilotInput{Mode: "find-ceiling", MaxDuration: "10s"}}, wantErr: "at least 30s"},
		{name: "server too small to search", in: StartInput{HTTPContent: validHTTP, Autopilot: &AutopilotInput{Mode: "find-ceiling"}},
			limits: func(l *Limits) { l.MaxDuration = 30 * time.Second }, wantErr: "at least 30s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := DefaultLimits()
			if c.limits != nil {
				c.limits(&l)
			}
			run, err := planRun(l, c.in)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("planRun: %v", err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(run.dir) })
			c.check(t, run)
		})
	}
}

// containsRun reports whether want appears in args as a consecutive run per
// flag pair, in any order of the pairs.
func containsRun(args, want []string) bool {
	for i := 0; i < len(want); i += 2 {
		found := false
		for j := 0; j+1 < len(args); j++ {
			if args[j] == want[i] && args[j+1] == want[i+1] {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestSteerTool_AutopilotStartShowsInTelemetryAndFreezesManualSteering(t *testing.T) {
	cs, st := steerClient(t, "normal", nil)

	var out SteerOutput
	res := call(t, cs, "steer_traffic", map[string]any{"action": "autopilot_start", "autopilot": map[string]any{"mode": "slo-guard", "max_p99_ms": 150}}, &out)
	if res.IsError || !out.Applied || out.Autopilot != "slo-guard" {
		t.Fatalf("autopilot_start = %+v %s", out, errText(res))
	}
	var tel session.Telemetry
	waitTelemetry(t, cs, st.SessionID, &tel, func() bool { return tel.Autopilot != nil && tel.Autopilot.State == "watching" })
	if tel.Autopilot.Mode != "slo-guard" {
		t.Errorf("autopilot = %+v", tel.Autopilot)
	}
	if len(tel.Steers) != 1 || tel.Steers[0].Action != "autopilot_start" || tel.Steers[0].Mode != "slo-guard" {
		t.Errorf("steers = %+v", tel.Steers)
	}

	for _, args := range []map[string]any{
		{"action": "hold_rps", "target_rps": 20},
		{"action": "release_hold"},
		{"action": "adjust_rps", "delta": 5},
		{"action": "set_rps", "target_rps": 30},
	} {
		res := call(t, cs, "steer_traffic", args, nil)
		if !res.IsError || !strings.Contains(errText(res), "autopilot_stop") {
			t.Errorf("%v during autopilot: isError=%v %q", args, res.IsError, errText(res))
		}
	}
	if res := call(t, cs, "steer_traffic", map[string]any{"action": "mark", "label": "looking"}, nil); res.IsError {
		t.Errorf("marks stay allowed: %s", errText(res))
	}

	res = call(t, cs, "steer_traffic", map[string]any{"action": "autopilot_stop"}, &out)
	if res.IsError || out.Autopilot != "stopped" || !strings.Contains(out.Note, "release_hold") {
		t.Fatalf("autopilot_stop = %+v %s", out, errText(res))
	}
	waitTelemetry(t, cs, st.SessionID, &tel, func() bool { return tel.Autopilot != nil && tel.Autopilot.Outcome == "stopped" })
	if res := call(t, cs, "steer_traffic", map[string]any{"action": "release_hold"}, &out); res.IsError {
		t.Errorf("release after the autopilot stopped: %s", errText(res))
	}
}

func TestSteerTool_AutopilotInputChecks(t *testing.T) {
	cs, _ := steerClient(t, "normal", nil)
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"no mode", map[string]any{"action": "autopilot_start"}, "needs autopilot.mode"},
		{"find-ceiling mid-run", map[string]any{"action": "autopilot_start", "autopilot": map[string]any{"mode": "find-ceiling"}}, "start_simulation"},
		{"unknown mode", map[string]any{"action": "autopilot_start", "autopilot": map[string]any{"mode": "x"}}, "slo-guard"},
		{"find-ceiling knobs", map[string]any{"action": "autopilot_start", "autopilot": map[string]any{"mode": "slo-guard", "max_rps": 9}}, "belong to find-ceiling"},
		{"stray delta", map[string]any{"action": "autopilot_start", "delta": 5, "autopilot": map[string]any{"mode": "slo-guard"}}, "only autopilot"},
		{"autopilot on another action", map[string]any{"action": "hold_rps", "target_rps": 5, "autopilot": map[string]any{"mode": "slo-guard"}}, "autopilot_start only"},
		{"stop with arguments", map[string]any{"action": "autopilot_stop", "delta": 1}, "no other arguments"},
	}
	for _, c := range cases {
		res := call(t, cs, "steer_traffic", c.args, nil)
		if !res.IsError || !strings.Contains(errText(res), c.want) {
			t.Errorf("%s: isError=%v %q, want containing %q", c.name, res.IsError, errText(res), c.want)
		}
	}
}

func TestSteerTool_AutopilotNeedsAnEngineThatHasOne(t *testing.T) {
	cs, _ := steerClient(t, "no-autopilot-cap", nil)
	for _, args := range []map[string]any{
		{"action": "autopilot_start", "autopilot": map[string]any{"mode": "slo-guard"}},
		{"action": "autopilot_stop"},
	} {
		res := call(t, cs, "steer_traffic", args, nil)
		if !res.IsError || !strings.Contains(errText(res), "control.autopilot") {
			t.Errorf("%v: isError=%v %q", args, res.IsError, errText(res))
		}
	}
}

func TestTelemetry_FinalCarriesTheAutopilotOutcome(t *testing.T) {
	cs, _ := simClient(t, "autopilot-ceiling", nil)
	var start StartOutput
	res := call(t, cs, "start_simulation", map[string]any{"http_content": validHTTP, "autopilot": map[string]any{"mode": "find-ceiling"}}, &start)
	if res.IsError || start.Autopilot != "find-ceiling" || !strings.Contains(start.Next, "autopilot") {
		t.Fatalf("start = %+v %s", start, errText(res))
	}
	var tel session.Telemetry
	waitTelemetry(t, cs, start.SessionID, &tel, func() bool { return tel.State == session.Finished })
	ap := tel.Autopilot
	if ap == nil || ap.Mode != "find-ceiling" || ap.State != "done" || ap.Outcome != "ceiling_found" || ap.CeilingRPS != 95 {
		t.Fatalf("final autopilot = %+v", ap)
	}
	if tel.Final == nil {
		t.Error("a finished run must carry its final statistics")
	}
	if len(tel.Marks) == 0 || !strings.Contains(tel.Marks[len(tel.Marks)-1].Label, "ceiling breached") {
		t.Errorf("autopilot marks should be listed: %+v", tel.Marks)
	}
}
