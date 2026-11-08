package mcp

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
	"github.com/shyam-s00/gopher-glide/internal/mcp/session/fakegg"
)

const validHTTP = "GET http://127.0.0.1:9/ok\n"

func TestPlanRun_Table(t *testing.T) {
	cfg := "stages:\n  - duration: 30s\n    target_rps: 50\n"
	cases := []struct {
		name    string
		in      StartInput
		limits  func(*Limits)
		wantErr string
		check   func(t *testing.T, r plannedRun)
	}{
		{name: "profile defaults", in: StartInput{HTTPContent: validHTTP, Profile: "load"},
			limits: func(l *Limits) { l.MaxRPS = 1000; l.MaxDuration = time.Hour },
			check: func(t *testing.T, r plannedRun) {
				if r.peakRPS != 100 || r.dur != 2*time.Minute {
					t.Errorf("peak=%d dur=%s", r.peakRPS, r.dur)
				}
				if !slices.Contains(r.args, "--control-exit-on-eof") || !slices.Contains(r.args, "--headless") {
					t.Errorf("supervised flags missing: %v", r.args)
				}
				if !(argAfter(r.args, "--profile") == "load") || slices.Contains(r.args, "--snap") {
					t.Errorf("args = %v", r.args)
				}
			}},
		{name: "profile scaled with snap", in: StartInput{HTTPContent: validHTTP, Profile: "load", PeakRPS: 40, Duration: "90s", Snap: true, SnapTag: "v1.2-rc"},
			check: func(t *testing.T, r plannedRun) {
				if r.peakRPS != 40 || r.dur != 90*time.Second {
					t.Errorf("peak=%d dur=%s", r.peakRPS, r.dur)
				}
				if argAfter(r.args, "--peak-rps") != "40" || argAfter(r.args, "--duration") != "1m30s" ||
					argAfter(r.args, "--snap-tag") != "v1.2-rc" || !slices.Contains(r.args, "--snap") {
					t.Errorf("args = %v", r.args)
				}
			}},
		{name: "config yaml puts the config path first", in: StartInput{HTTPContent: validHTTP, ConfigYAML: cfg},
			check: func(t *testing.T, r plannedRun) {
				if r.peakRPS != 50 || r.dur != 30*time.Second || !strings.HasSuffix(r.args[0], configFileName) {
					t.Errorf("peak=%d dur=%s args=%v", r.peakRPS, r.dur, r.args)
				}
				b, err := os.ReadFile(r.args[0])
				if err != nil || !strings.Contains(string(b), "httpFile: "+httpFileName) {
					t.Errorf("config must point at the supplied file: %v\n%s", err, b)
				}
			}},
		{name: "http from path", in: StartInput{HTTPPath: writeTemp(t, validHTTP), Profile: "smoke"}},

		{name: "no http source", in: StartInput{Profile: "smoke"}, wantErr: "content"},
		{name: "no shape", in: StartInput{HTTPContent: validHTTP}, wantErr: "load shape"},
		{name: "both shapes", in: StartInput{HTTPContent: validHTTP, Profile: "smoke", ConfigYAML: cfg}, wantErr: "not both"},
		{name: "profile knobs with config", in: StartInput{HTTPContent: validHTTP, ConfigYAML: cfg, PeakRPS: 5}, wantErr: "apply to a profile"},
		{name: "unknown profile", in: StartInput{HTTPContent: validHTTP, Profile: "nope"}, wantErr: "unknown profile"},
		{name: "profile as a path is not a built-in", in: StartInput{HTTPContent: validHTTP, Profile: "../x.yaml"}, wantErr: "unknown profile"},
		{name: "bad duration", in: StartInput{HTTPContent: validHTTP, Profile: "smoke", Duration: "soon"}, wantErr: "duration"},
		{name: "negative peak", in: StartInput{HTTPContent: validHTTP, Profile: "smoke", PeakRPS: -1}, wantErr: "peak_rps"},
		{name: "invalid yaml", in: StartInput{HTTPContent: validHTTP, ConfigYAML: "stages: ["}, wantErr: "not valid YAML"},
		{name: "config without stages", in: StartInput{HTTPContent: validHTTP, ConfigYAML: "config:\n  jitter: 0.1\n"}, wantErr: "config_yaml is invalid"},
		{name: "invalid http file", in: StartInput{HTTPContent: "GET /relative\n", Profile: "smoke"}, wantErr: "line 1"},
		{name: "snap tag with slash", in: StartInput{HTTPContent: validHTTP, Profile: "smoke", Snap: true, SnapTag: "../../etc"}, wantErr: "snap_tag"},
		{name: "snap tag without snap", in: StartInput{HTTPContent: validHTTP, Profile: "smoke", SnapTag: "x"}, wantErr: "snap: true"},

		// Edge 19: caps, each naming the flag that raises it.
		{name: "over max rps", in: StartInput{HTTPContent: validHTTP, Profile: "load", PeakRPS: 5000}, wantErr: "--max-rps"},
		{name: "over max duration", in: StartInput{HTTPContent: validHTTP, Profile: "load", Duration: "3h"}, wantErr: "--max-duration"},
		{name: "profile default over cap", in: StartInput{HTTPContent: validHTTP, Profile: "black-friday"}, wantErr: "--max-rps"},
		{name: "config over cap", in: StartInput{HTTPContent: validHTTP, ConfigYAML: "stages:\n  - duration: 20m\n    target_rps: 2000\n"}, wantErr: "--max-duration"},
		{name: "both caps reported", in: StartInput{HTTPContent: validHTTP, Profile: "load", PeakRPS: 5000, Duration: "3h"}, wantErr: "--max-duration"},
		{name: "cap boundary is allowed", in: StartInput{HTTPContent: validHTTP, Profile: "load", PeakRPS: 1000, Duration: "10m"}},

		// Edge 20: target hosts.
		{name: "host not allowed", in: StartInput{HTTPContent: "GET http://evil.example.com/x\n", Profile: "smoke"},
			limits: func(l *Limits) { l.AllowHosts = []string{"api.example.com"} }, wantErr: "--allow-host"},
		{name: "host allowed", in: StartInput{HTTPContent: "GET http://api.example.com/x\n", Profile: "smoke"},
			limits: func(l *Limits) { l.AllowHosts = []string{"api.example.com"} }},
		{name: "variable host cannot be verified", in: StartInput{HTTPContent: "GET http://x.example.com/a\n# @gg-export h = regex: (.*)\n\n###\nGET http://{{h}}/b\n", Profile: "smoke"},
			limits: func(l *Limits) { l.AllowHosts = []string{"x.example.com"} }, wantErr: "host comes from a variable"},
		{name: "variable in path is fine with an allowlist", in: StartInput{HTTPContent: "GET http://x.example.com/a\n# @gg-export h = regex: (.*)\n\n###\nGET http://x.example.com/{{h}}\n", Profile: "smoke"},
			limits: func(l *Limits) { l.AllowHosts = []string{"x.example.com"} }},
		{name: "variable host without an allowlist is fine", in: StartInput{HTTPContent: "GET http://x.example.com/a\n# @gg-export h = regex: (.*)\n\n###\nGET http://{{h}}/b\n", Profile: "smoke"}},
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
				if run.dir != "" {
					if _, serr := os.Stat(run.dir); !os.IsNotExist(serr) {
						t.Errorf("a rejected request must not leave %s behind", run.dir)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(run.dir) })
			if _, serr := os.Stat(run.dir + "/" + httpFileName); serr != nil {
				t.Errorf("http file not written: %v", serr)
			}
			if c.check != nil {
				c.check(t, run)
			}
		})
	}
}

func argAfter(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := t.TempDir() + "/in.http"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// simClient connects a client to a server whose simulations are the fake child.
func simClient(t *testing.T, scenario string, mutate func(*Options)) (*sdk.ClientSession, *Server) {
	t.Helper()
	t.Setenv(fakegg.Env, scenario)
	opts := Options{
		Limits: DefaultLimits(), Version: "test", Exe: os.Args[0],
		Sessions: session.ManagerOptions{
			StartTimeout: 5 * time.Second,
			Stop:         session.StopOptions{Graceful: 2 * time.Second, Term: time.Second},
		},
	}
	if mutate != nil {
		mutate(&opts)
	}
	srv, err := NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	ct, st := sdk.NewInMemoryTransports()
	ss, err := srv.SDK.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close(); _ = cs.Close(); _ = ss.Wait() })
	return cs, srv
}

func startArgs() map[string]any {
	return map[string]any{"http_content": validHTTP, "profile": "load", "peak_rps": 20, "duration": "30s"}
}

func startOK(t *testing.T, cs *sdk.ClientSession) StartOutput {
	t.Helper()
	var out StartOutput
	res := call(t, cs, "start_simulation", startArgs(), &out)
	if res.IsError {
		t.Fatalf("start_simulation: %s", errText(res))
	}
	return out
}

func TestSimTools_ListedWithGuidance(t *testing.T) {
	cs, _ := simClient(t, "normal", nil)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*sdk.Tool{}
	for _, tl := range list.Tools {
		got[tl.Name] = tl
	}
	for _, n := range []string{"start_simulation", "get_live_telemetry", "stop_simulation"} {
		tl := got[n]
		if tl == nil || len(tl.Description) < 100 {
			t.Errorf("tool %s missing or thinly described", n)
		}
	}
	if a := got["start_simulation"].Annotations; a == nil || a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Errorf("start_simulation sends real traffic and must say so: %+v", a)
	}
	if !got["get_live_telemetry"].Annotations.ReadOnlyHint {
		t.Error("telemetry is read-only")
	}
}

func TestSimTools_StartTelemetryStop(t *testing.T) {
	cs, _ := simClient(t, "normal", nil)
	out := startOK(t, cs)
	if !strings.HasPrefix(out.SessionID, "sim_") || out.PeakRPS != 20 || out.DurationS != 30 || len(out.Stages) != 2 || out.Stages[0].Name != "Ramp Up" {
		t.Fatalf("start output = %+v", out)
	}

	var tel session.Telemetry
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		call(t, cs, "get_live_telemetry", map[string]any{"session_id": out.SessionID}, &tel)
		if tel.State == session.Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if tel.State != session.Running || tel.Metrics == nil || tel.Metrics.TargetRPS != 10 {
		t.Fatalf("telemetry = %+v", tel)
	}

	var final session.Telemetry
	call(t, cs, "stop_simulation", map[string]any{"session_id": out.SessionID}, &final)
	if final.State != session.Stopped || final.Final == nil || final.Final.Total != 100 || final.SnapshotPath == "" {
		t.Fatalf("final = %+v", final)
	}

	// Stopping again, or asking about it, still works.
	var again session.Telemetry
	res := call(t, cs, "stop_simulation", map[string]any{"session_id": out.SessionID}, &again)
	if res.IsError || again.State != session.Stopped {
		t.Fatalf("second stop: %v %+v", errText(res), again)
	}
}

func TestSimTools_SessionIDOptionalWhenOnlyOne(t *testing.T) {
	cs, _ := simClient(t, "normal", nil)
	out := startOK(t, cs)
	var tel session.Telemetry
	call(t, cs, "get_live_telemetry", map[string]any{}, &tel)
	if tel.SessionID != out.SessionID {
		t.Fatalf("telemetry for %q, want %q", tel.SessionID, out.SessionID)
	}
	call(t, cs, "stop_simulation", map[string]any{}, &tel)
	if tel.State != session.Stopped {
		t.Fatalf("state = %s", tel.State)
	}
}

func TestSimTools_UnknownSessionIsAStructuredError(t *testing.T) {
	cs, _ := simClient(t, "normal", nil)
	for _, tool := range []string{"get_live_telemetry", "stop_simulation"} {
		res := call(t, cs, tool, map[string]any{"session_id": "sim_nope"}, nil)
		if !res.IsError || !strings.Contains(errText(res), `unknown session_id "sim_nope"`) {
			t.Errorf("%s: isError=%v text=%q", tool, res.IsError, errText(res))
		}
	}
	res := call(t, cs, "get_live_telemetry", map[string]any{}, nil)
	if !res.IsError || !strings.Contains(errText(res), "session_id is required") {
		t.Errorf("no sessions at all: %q", errText(res))
	}
}

// Edge 19: the third simulation is refused, naming the flag and the running ones.
func TestSimTools_SessionCap(t *testing.T) {
	cs, _ := simClient(t, "normal", func(o *Options) { o.Limits.MaxSessions = 2 })
	a, b := startOK(t, cs), startOK(t, cs)
	res := call(t, cs, "start_simulation", startArgs(), nil)
	text := errText(res)
	if !res.IsError || !strings.Contains(text, "--max-sessions") || !strings.Contains(text, a.SessionID) || !strings.Contains(text, b.SessionID) {
		t.Fatalf("isError=%v text=%q", res.IsError, text)
	}
	call(t, cs, "stop_simulation", map[string]any{"session_id": a.SessionID}, nil)
	startOK(t, cs)
}

func TestSimTools_CapsAndHostsRefuseBeforeSpawning(t *testing.T) {
	cs, srv := simClient(t, "normal", func(o *Options) {
		o.Limits.MaxRPS = 50
		o.Limits.AllowHosts = []string{"127.0.0.1:9"}
	})
	for name, args := range map[string]map[string]any{
		"rps":  {"http_content": validHTTP, "profile": "load", "peak_rps": 500},
		"host": {"http_content": "GET http://other.example.com/\n", "profile": "load", "peak_rps": 5},
	} {
		res := call(t, cs, "start_simulation", args, nil)
		if !res.IsError || !(strings.Contains(errText(res), "--max-rps") || strings.Contains(errText(res), "--allow-host")) {
			t.Errorf("%s: isError=%v text=%q", name, res.IsError, errText(res))
		}
	}
	if ids := srv.sessions.IDs(); len(ids) != 0 {
		t.Errorf("nothing may be spawned for refused requests: %v", ids)
	}
}

func TestSimTools_StartFailureReportsChildError(t *testing.T) {
	cs, srv := simClient(t, "no-started", nil)
	res := call(t, cs, "start_simulation", startArgs(), nil)
	if !res.IsError || !strings.Contains(errText(res), "no stages defined") {
		t.Fatalf("isError=%v text=%q", res.IsError, errText(res))
	}
	if len(srv.sessions.IDs()) != 0 {
		t.Error("failed start left a session")
	}
}

// Edge 22/24 through the tool: a crashed run still answers.
func TestSimTools_CrashedRunStillAnswers(t *testing.T) {
	cs, _ := simClient(t, "die-after-start", nil)
	out := startOK(t, cs)
	var tel session.Telemetry
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && tel.State != session.Failed {
		call(t, cs, "get_live_telemetry", map[string]any{"session_id": out.SessionID}, &tel)
		time.Sleep(20 * time.Millisecond)
	}
	if tel.State != session.Failed || !strings.Contains(tel.StderrTail, "panic: boom") {
		t.Fatalf("telemetry = %+v", tel)
	}
}

func TestSimTools_StartingStateHasNoNumbers(t *testing.T) {
	cs, _ := simClient(t, "no-heartbeat", nil)
	out := startOK(t, cs)
	var tel session.Telemetry
	call(t, cs, "get_live_telemetry", map[string]any{"session_id": out.SessionID}, &tel)
	if tel.State != session.Starting || tel.Metrics != nil {
		t.Fatalf("telemetry = %+v", tel)
	}
}

// Edge 23: the server going away (EOF/cancel) stops every simulation it started.
func TestServer_ShutdownStopsSimulations(t *testing.T) {
	t.Setenv(fakegg.Env, "ignore-stop")
	srv, err := NewServer(Options{Limits: DefaultLimits(), Exe: os.Args[0],
		Sessions: session.ManagerOptions{StartTimeout: 5 * time.Second, Stop: session.StopOptions{Graceful: 300 * time.Millisecond, Term: 300 * time.Millisecond}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ct, st := sdk.NewInMemoryTransports()
	ss, err := srv.SDK.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := startOK(t, cs)
	sess, _ := srv.sessions.Get(out.SessionID)

	_ = cs.Close()
	_ = ss.Wait()
	srv.Close()

	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("simulation still running after the server closed")
	}
}

func TestServe_StopsSimulationsOnStdinEOF(t *testing.T) {
	t.Setenv(fakegg.Env, "normal")
	srv, err := NewServer(Options{Limits: DefaultLimits(), Exe: os.Args[0],
		Sessions: session.ManagerOptions{StartTimeout: 5 * time.Second, Stop: session.StopOptions{Graceful: 2 * time.Second, Term: time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	// Start a session directly through the manager, then end the stream.
	run, err := planRun(DefaultLimits(), StartInput{HTTPContent: validHTTP, Profile: "smoke"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := srv.sessions.Start(context.Background(), session.Spec{Exe: os.Args[0], Args: []string{"-test.run=^$"}, Dir: run.dir})
	if err != nil {
		t.Fatal(err)
	}
	in, inW := pipePair()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), in, nopWriteCloser{}) }()
	_ = inW.Close() // EOF

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
	select {
	case <-sess.Done():
	default:
		t.Fatal("Serve returned with a simulation still running")
	}
}

func steerClient(t *testing.T, scenario string, mutate func(*Options)) (*sdk.ClientSession, StartOutput) {
	t.Helper()
	cs, _ := simClient(t, scenario, func(o *Options) {
		o.Sessions.SteerSettle = 60 * time.Millisecond
		o.Sessions.ReplyTimeout = 400 * time.Millisecond
		if mutate != nil {
			mutate(o)
		}
	})
	out := startOK(t, cs)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var tel session.Telemetry
		call(t, cs, "get_live_telemetry", map[string]any{"session_id": out.SessionID}, &tel)
		if tel.State == session.Running {
			return cs, out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("never reached running")
	return nil, out
}

func TestSteerTool_ListedAndHonestAboutEffects(t *testing.T) {
	cs, _ := simClient(t, "normal", nil)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range list.Tools {
		if tl.Name != "steer_traffic" {
			continue
		}
		if len(tl.Description) < 100 || tl.Annotations == nil || tl.Annotations.DestructiveHint == nil || !*tl.Annotations.DestructiveHint {
			t.Errorf("steer_traffic changes real load and must say so: %+v", tl)
		}
		return
	}
	t.Fatal("steer_traffic is not offered")
}

func TestSteerTool_AdjustThenSetMovesTheTarget(t *testing.T) {
	cs, st := steerClient(t, "normal", nil)

	var out SteerOutput
	res := call(t, cs, "steer_traffic", map[string]any{"action": "adjust_rps", "delta": 6}, &out)
	if res.IsError || !out.Applied || out.Delta == nil || *out.Delta != 6 || *out.BiasTotal != 6 || out.TargetRPSBefore != 10 || !strings.Contains(out.Note, "1 s") {
		t.Fatalf("adjust = %+v %s", out, errText(res))
	}

	var tel session.Telemetry
	waitTelemetry(t, cs, st.SessionID, &tel, func() bool { return tel.Metrics != nil && tel.Metrics.TargetRPS == 16 })
	if len(tel.Steers) != 1 || tel.Steers[0].Delta != 6 {
		t.Errorf("steers = %+v", tel.Steers)
	}

	time.Sleep(150 * time.Millisecond) // let a heartbeat newer than the change arrive
	res = call(t, cs, "steer_traffic", map[string]any{"action": "set_rps", "target_rps": 4}, &out)
	if res.IsError || *out.Delta != -12 || *out.BiasTotal != -6 || out.TargetRPSBefore != 16 {
		t.Fatalf("set = %+v %s", out, errText(res))
	}
	waitTelemetry(t, cs, st.SessionID, &tel, func() bool { return tel.Metrics != nil && tel.Metrics.TargetRPS == 4 })

	// Already there: nothing is sent.
	time.Sleep(150 * time.Millisecond)
	res = call(t, cs, "steer_traffic", map[string]any{"action": "set_rps", "target_rps": 4}, &out)
	if res.IsError || out.Applied || *out.Delta != 0 || !strings.Contains(out.Note, "already") {
		t.Fatalf("no-op set = %+v %s", out, errText(res))
	}
}

func waitTelemetry(t *testing.T, cs *sdk.ClientSession, id string, tel *session.Telemetry, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		call(t, cs, "get_live_telemetry", map[string]any{"session_id": id}, tel)
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition never held: %+v", tel)
}

func TestSteerTool_RefusesWhatBreaksTheCaps(t *testing.T) {
	cs, _ := steerClient(t, "normal", func(o *Options) { o.Limits.MaxRPS = 100 })
	for _, args := range []map[string]any{
		{"action": "set_rps", "target_rps": 101},
		{"action": "set_rps", "target_rps": 0},
		{"action": "adjust_rps", "delta": 200},
	} {
		res := call(t, cs, "steer_traffic", args, nil)
		if !res.IsError || !strings.Contains(errText(res), "100") {
			t.Errorf("%v: isError=%v %q", args, res.IsError, errText(res))
		}
	}
	var tel session.Telemetry
	call(t, cs, "get_live_telemetry", map[string]any{}, &tel)
	if len(tel.Steers) != 0 || tel.Metrics.Bias != 0 {
		t.Errorf("refused changes must leave no trace: %+v", tel)
	}
}

func TestSteerTool_MarkHonoursOffsetAndReportsClamp(t *testing.T) {
	cs, st := steerClient(t, "clamp", nil)
	var out SteerOutput
	res := call(t, cs, "steer_traffic", map[string]any{"action": "mark", "label": "spike seen", "at_elapsed_s": 0.01}, &out)
	if res.IsError || !out.Applied || out.ElapsedS == nil || *out.ElapsedS != 0.01 || out.Clamped || out.Label != "spike seen" {
		t.Fatalf("mark = %+v %s", out, errText(res))
	}
	res = call(t, cs, "steer_traffic", map[string]any{"action": "mark", "label": "future", "at_elapsed_s": 5000}, &out)
	if res.IsError || !out.Clamped || *out.ElapsedS >= 5000 || out.Warning == "" {
		t.Fatalf("clamped mark = %+v %s", out, errText(res))
	}
	var tel session.Telemetry
	waitTelemetry(t, cs, st.SessionID, &tel, func() bool { return len(tel.Marks) == 2 })
}

func TestSteerTool_MarkWithoutCapabilityWarns(t *testing.T) {
	cs, _ := steerClient(t, "no-elapsed-cap", nil)
	var out SteerOutput
	res := call(t, cs, "steer_traffic", map[string]any{"action": "mark", "label": "x", "at_elapsed_s": 3}, &out)
	if res.IsError || out.Warning == "" || *out.ElapsedS == 3 {
		t.Fatalf("mark = %+v %s", out, errText(res))
	}
}

// Edge 25 through the tool: after stop, steering is refused by name.
func TestSteerTool_NotRunningAfterStop(t *testing.T) {
	cs, st := steerClient(t, "normal", nil)
	call(t, cs, "stop_simulation", map[string]any{"session_id": st.SessionID}, nil)
	for _, args := range []map[string]any{
		{"action": "adjust_rps", "delta": 5},
		{"action": "mark", "label": "late"},
	} {
		res := call(t, cs, "steer_traffic", args, nil)
		if !res.IsError || !strings.Contains(errText(res), "session_not_running") {
			t.Errorf("%v: isError=%v %q", args, res.IsError, errText(res))
		}
	}
}

func TestSteerTool_InputAndSessionErrors(t *testing.T) {
	cs, _ := steerClient(t, "normal", nil)
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "pause"}, "unknown action"},
		{map[string]any{"action": "adjust_rps"}, "non-zero delta"},
		{map[string]any{"action": "mark"}, "needs a label"},
		{map[string]any{"action": "mark", "label": "x", "session_id": "sim_nope"}, "unknown session_id"},
	} {
		res := call(t, cs, "steer_traffic", c.args, nil)
		if !res.IsError || !strings.Contains(errText(res), c.want) {
			t.Errorf("%v: isError=%v %q, want %q", c.args, res.IsError, errText(res), c.want)
		}
	}
}

func TestSteerTool_RejectionAndSilenceAreReported(t *testing.T) {
	cs, _ := steerClient(t, "reject-bias", nil)
	res := call(t, cs, "steer_traffic", map[string]any{"action": "adjust_rps", "delta": 5}, nil)
	if !res.IsError || !strings.Contains(errText(res), "invalid_argument") {
		t.Errorf("rejection: %q", errText(res))
	}

	cs, _ = steerClient(t, "no-reply", nil)
	res = call(t, cs, "steer_traffic", map[string]any{"action": "adjust_rps", "delta": 5}, nil)
	if !res.IsError || !strings.Contains(errText(res), "did not reply") {
		t.Errorf("silence: %q", errText(res))
	}
}

// Parallel callers cannot jointly push the bias past the cap.
func TestSteerTool_ConcurrentAdjustsRespectTheCap(t *testing.T) {
	cs, st := steerClient(t, "slow-ack", func(o *Options) {
		o.Limits.MaxRPS = 40
		o.Sessions.ReplyTimeout = 5 * time.Second
	})
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "steer_traffic", Arguments: map[string]any{"action": "adjust_rps", "delta": 5}})
			if err == nil && !res.IsError {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	// Peak 20 + bias must stay <= 40, so only four +5 steps fit.
	var tel session.Telemetry
	waitTelemetry(t, cs, st.SessionID, &tel, func() bool { return tel.Metrics != nil && tel.Metrics.Bias == 20 })
	if ok.Load() != 4 {
		t.Errorf("%d adjustments succeeded, want exactly 4", ok.Load())
	}
}
