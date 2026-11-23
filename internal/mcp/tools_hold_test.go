package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
)

func TestSteerTool_HoldPinsTheRateAndReleaseReturnsIt(t *testing.T) {
	cs, st := steerClient(t, "normal", nil)

	var out SteerOutput
	res := call(t, cs, "steer_traffic", map[string]any{"action": "hold_rps", "target_rps": 25}, &out)
	if res.IsError || !out.Applied || out.HoldRPS == nil || *out.HoldRPS != 25 || !strings.Contains(out.Note, "release_hold") {
		t.Fatalf("hold = %+v %s", out, errText(res))
	}
	var tel session.Telemetry
	waitTelemetry(t, cs, st.SessionID, &tel, func() bool {
		return tel.HoldRPS == 25 && tel.Metrics != nil && tel.Metrics.TargetRPS == 25
	})
	if len(tel.Steers) != 1 || tel.Steers[0].Action != "hold_rps" || tel.Steers[0].HoldRPS != 25 {
		t.Errorf("steers = %+v", tel.Steers)
	}

	var rel SteerOutput
	res = call(t, cs, "steer_traffic", map[string]any{"action": "release_hold"}, &rel)
	if res.IsError || !rel.Applied || rel.HoldRPS != nil {
		t.Fatalf("release = %+v %s", rel, errText(res))
	}
	waitTelemetry(t, cs, st.SessionID, &tel, func() bool { return tel.HoldRPS == 0 && tel.Metrics != nil && tel.Metrics.TargetRPS == 10 })
}

func TestSteerTool_BiasChangesAreRefusedWhileHeld(t *testing.T) {
	cs, _ := steerClient(t, "normal", nil)
	call(t, cs, "steer_traffic", map[string]any{"action": "hold_rps", "target_rps": 25}, &SteerOutput{})
	time.Sleep(150 * time.Millisecond)
	for _, args := range []map[string]any{
		{"action": "adjust_rps", "delta": 5},
		{"action": "set_rps", "target_rps": 30},
	} {
		res := call(t, cs, "steer_traffic", args, nil)
		if !res.IsError || !strings.Contains(errText(res), "held at 25") || !strings.Contains(errText(res), "release_hold") {
			t.Errorf("%v: isError=%v %q", args, res.IsError, errText(res))
		}
	}
	// A second hold re-pins; release then makes bias changes work again.
	var out SteerOutput
	if res := call(t, cs, "steer_traffic", map[string]any{"action": "hold_rps", "target_rps": 30}, &out); res.IsError || *out.HoldRPS != 30 {
		t.Fatalf("re-hold = %+v %s", out, errText(res))
	}
	call(t, cs, "steer_traffic", map[string]any{"action": "release_hold"}, &out)
	if res := call(t, cs, "steer_traffic", map[string]any{"action": "adjust_rps", "delta": 5}, &out); res.IsError {
		t.Fatalf("adjust after release: %s", errText(res))
	}
}

func TestSteerTool_HoldRespectsTheRateCap(t *testing.T) {
	cs, _ := steerClient(t, "normal", func(o *Options) { o.Limits.MaxRPS = 100 })
	for _, rps := range []int{101, -3} {
		res := call(t, cs, "steer_traffic", map[string]any{"action": "hold_rps", "target_rps": rps}, nil)
		if !res.IsError || !strings.Contains(errText(res), "100") {
			t.Errorf("target %d: isError=%v %q", rps, res.IsError, errText(res))
		}
	}
	var out SteerOutput
	if res := call(t, cs, "steer_traffic", map[string]any{"action": "hold_rps", "target_rps": 100}, &out); res.IsError {
		t.Errorf("the cap itself is allowed: %s", errText(res))
	}
	var tel session.Telemetry
	call(t, cs, "get_live_telemetry", map[string]any{}, &tel)
	if len(tel.Steers) != 1 {
		t.Errorf("refused holds must leave no trace: %+v", tel.Steers)
	}
}

func TestSteerTool_HoldNeedsAnEngineThatCanHold(t *testing.T) {
	cs, _ := steerClient(t, "no-hold-cap", nil)
	res := call(t, cs, "steer_traffic", map[string]any{"action": "hold_rps", "target_rps": 10}, nil)
	if !res.IsError || !strings.Contains(errText(res), "control.hold") {
		t.Errorf("isError=%v %q", res.IsError, errText(res))
	}
}

func TestSteerTool_EveryCommandNamesTheAgentAsSource(t *testing.T) {
	cs, _ := steerClient(t, "require-agent-source", nil)
	for _, args := range []map[string]any{
		{"action": "hold_rps", "target_rps": 12},
		{"action": "release_hold"},
		{"action": "mark", "label": "x"},
		{"action": "adjust_rps", "delta": 3},
	} {
		if res := call(t, cs, "steer_traffic", args, nil); res.IsError {
			t.Errorf("%v: %s", args, errText(res))
		}
	}
}

func TestTelemetryTool_WaitWakesOnADegradedWindow(t *testing.T) {
	cs, st := steerClient(t, "degrade", nil)
	begin := time.Now()
	var tel session.Telemetry
	res := call(t, cs, "get_live_telemetry", map[string]any{"session_id": st.SessionID, "wait_s": 10, "wake_on_p99_ms": 250}, &tel)
	if res.IsError {
		t.Fatal(errText(res))
	}
	w := tel.WindowSummary
	if w == nil || w.WakeReason != "p99_threshold" || time.Since(begin) > 5*time.Second || w.WaitedS <= 0 {
		t.Fatalf("window_summary = %+v after %v", w, time.Since(begin))
	}
	if tel.Window == nil || tel.Window.P99Ms != 300 || tel.State != session.Running {
		t.Errorf("telemetry after waking = %+v", tel)
	}
}

func TestTelemetryTool_NoWaitHasNoSummary(t *testing.T) {
	cs, st := steerClient(t, "normal", nil)
	var tel session.Telemetry
	call(t, cs, "get_live_telemetry", map[string]any{"session_id": st.SessionID}, &tel)
	if tel.WindowSummary != nil {
		t.Errorf("an immediate read must not carry a summary: %+v", tel.WindowSummary)
	}
}

func TestTelemetryTool_WaitTimerAndRunEnd(t *testing.T) {
	cs, st := steerClient(t, "normal", nil)
	var tel session.Telemetry
	call(t, cs, "get_live_telemetry", map[string]any{"session_id": st.SessionID, "wait_s": 0.3}, &tel)
	if w := tel.WindowSummary; w == nil || w.WakeReason != "timer" || w.Beats == 0 {
		t.Fatalf("timer wait = %+v", w)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		call(t, cs, "stop_simulation", map[string]any{"session_id": st.SessionID}, nil)
	}()
	call(t, cs, "get_live_telemetry", map[string]any{"session_id": st.SessionID, "wait_s": 20}, &tel)
	if w := tel.WindowSummary; w == nil || w.WakeReason != "run_ended" || tel.State != session.Stopped {
		t.Fatalf("run-end wait = %+v state %s", w, tel.State)
	}
}

func TestTelemetryTool_RejectsBadWaits(t *testing.T) {
	cs, st := steerClient(t, "normal", nil)
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"negative wait", map[string]any{"wait_s": -1}, "between 0 and 30"},
		{"too long", map[string]any{"wait_s": 31}, "between 0 and 30"},
		{"wake without wait", map[string]any{"wake_on_p99_ms": 100}, "need wait_s"},
		{"any error without wait", map[string]any{"wake_on_any_error": true}, "need wait_s"},
		{"error rate above 1", map[string]any{"wait_s": 1, "wake_on_error_rate": 1.5}, "between 0 and 1"},
		{"negative p99", map[string]any{"wait_s": 1, "wake_on_p99_ms": -5}, "positive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.args["session_id"] = st.SessionID
			res := call(t, cs, "get_live_telemetry", c.args, nil)
			if !res.IsError || !strings.Contains(errText(res), c.want) {
				t.Errorf("isError=%v %q, want %q", res.IsError, errText(res), c.want)
			}
		})
	}
}

func TestTelemetryInput_Watch(t *testing.T) {
	w, err := TelemetryInput{WaitS: 2.5, WakeOnP99Ms: 200, WakeOnErrors: 0.1, WakeOnAny: true}.watch()
	if err != nil || w.Wait != 2500*time.Millisecond || w.WakeP99Ms != 200 || w.WakeErrRate != 0.1 || !w.WakeAnyError {
		t.Errorf("watch = %+v err = %v", w, err)
	}
	if w, err := (TelemetryInput{}).watch(); err != nil || w.Wait != 0 {
		t.Errorf("empty input = %+v err = %v", w, err)
	}
}
