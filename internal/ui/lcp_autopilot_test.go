package ui

import (
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/autopilot"
	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
)

// apRunner is a holdRunner that reports a fixed current rate. Its hold is
// atomic because the controller and the renderer both write it.
type apRunner struct {
	holdRunner
	rate float64
	pin  atomic.Int64
}

func (r *apRunner) HoldRPS(rps int) { r.pin.Store(int64(rps)) }
func (r *apRunner) ReleaseHold()    { r.pin.Store(0) }
func (r *apRunner) GetHoldRPS() int { return int(r.pin.Load()) }

func (r *apRunner) GetMetrics() *engine.MetricsSnapshot {
	return &engine.MetricsSnapshot{Throughput: r.rate, TargetRPS: int(r.rate)}
}

var _ autopilot.Target = &apRunner{}

func newAPRunner() *apRunner {
	return &apRunner{holdRunner: holdRunner{fixedStartRunner: fixedStartRunner{start: time.Now().Add(-time.Second)}}, rate: 100}
}

func hasCap(r *HeadlessRenderer, eng engine.Runner, name string) bool {
	for _, c := range *r.capabilities(eng) {
		if c == name {
			return true
		}
	}
	return false
}

func TestHeadless_AutopilotCapability_OnlyWhenEngineCanBeDriven(t *testing.T) {
	r := &HeadlessRenderer{ControlMode: "stdin"}
	if !hasCap(r, newAPRunner(), "control.autopilot") {
		t.Error("autopilot-capable engine must advertise control.autopilot")
	}
	if hasCap(r, &blockingRunner{}, "control.autopilot") {
		t.Error("plain runner must not advertise control.autopilot")
	}
	if hasCap(&HeadlessRenderer{ControlMode: "none"}, newAPRunner(), "control.autopilot") {
		t.Error("control none must advertise nothing")
	}
}

func TestHeadless_Autopilot_StartObserveStopAndHandBack(t *testing.T) {
	rb := newAPRunner()
	events, r := runControlScript(t, rb,
		`{"id":"a1","command":"autopilot","action":"start","mode":"slo-guard","max_p99_ms":200}`,
		`{"id":"h1","command":"hold","rps":50}`,
		`{"id":"b1","command":"bias","amount":5}`,
		`{"id":"r1","command":"release"}`,
		`{"id":"a2","command":"autopilot","action":"stop"}`,
		`{"id":"h2","command":"hold","rps":50}`,
	)

	var rejected []string
	for _, e := range eventsNamed(events, "error") {
		if e["reason"] == "autopilot_active" {
			rejected = append(rejected, e["id"].(string))
		} else {
			t.Errorf("unexpected error %v", e)
		}
	}
	if strings.Join(rejected, ",") != "h1,b1,r1" {
		t.Errorf("rejected commands = %v, want h1,b1,r1", rejected)
	}

	acks := eventsNamed(events, "ack")
	byID := map[string]map[string]any{}
	for _, a := range acks {
		if id, _ := a["id"].(string); id != "" {
			byID[id] = a
		}
	}
	if byID["a1"]["mode"] != "slo-guard" || byID["a2"] == nil {
		t.Errorf("autopilot acks = %v", byID)
	}
	if byID["h2"]["hold_rps"] != 50.0 {
		t.Errorf("hold after the autopilot stopped should be accepted: %v", byID["h2"])
	}

	var states []string
	for _, e := range eventsNamed(events, "autopilot") {
		states = append(states, e["state"].(string))
	}
	if len(states) < 2 || states[0] != "watching" || states[len(states)-1] != "done" {
		t.Errorf("autopilot states = %v, want watching ... done", states)
	}
	closing := eventsNamed(events, "autopilot")[len(states)-1]
	if closing["outcome"] != "stopped" {
		t.Errorf("closing event = %v", closing)
	}
	if stopped := eventsNamed(events, "stopped"); len(stopped) != 1 || stopped[0]["mode"] != "slo-guard" || stopped[0]["outcome"] != "stopped" {
		t.Errorf("terminal event = %v", stopped)
	}
	if meta := r.AutopilotMeta(); meta == nil || meta.Mode != "slo-guard" || meta.Outcome != "stopped" {
		t.Errorf("snapshot meta = %+v", meta)
	}
	if len(r.HoldEvents()) != 1 || r.HoldEvents()[0].RPS != 50 {
		t.Errorf("hold events = %+v", r.HoldEvents())
	}
}

func TestHeadless_Autopilot_BadStartsAreRejected(t *testing.T) {
	events, _ := runControlScript(t, newAPRunner(),
		`{"id":"e1","command":"autopilot","action":"start","mode":"find-ceiling"}`,
		`{"id":"e2","command":"autopilot","action":"start","mode":"bogus"}`,
		`{"id":"e3","command":"autopilot","action":"start","mode":"slo-guard","max_error_rate":5}`,
		`{"id":"e4","command":"autopilot","action":"stop"}`,
		`{"id":"e5","command":"autopilot","action":"pause"}`,
		`{"id":"e6","command":"autopilot","action":"start","mode":"slo-guard","max_p99_ms":-1}`,
		`{"id":"ok","command":"autopilot","action":"start","mode":"slo-guard"}`,
		`{"id":"e7","command":"autopilot","action":"start","mode":"slo-guard"}`,
	)
	want := map[string]string{
		"e1": "invalid_argument", "e2": "invalid_argument", "e3": "invalid_argument",
		"e4": "invalid_argument", "e5": "invalid_argument", "e6": "invalid_argument", "e7": "autopilot_active",
	}
	got := map[string]string{}
	for _, e := range eventsNamed(events, "error") {
		got[e["id"].(string)] = e["reason"].(string)
	}
	for id, reason := range want {
		if got[id] != reason {
			t.Errorf("%s: reason = %q, want %q (all: %v)", id, got[id], reason, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("errors = %v", got)
	}
}

func TestHeadless_Autopilot_UnsupportedEngineAnswersUnknownCommand(t *testing.T) {
	events, _ := runControlScript(t, &blockingRunner{}, `{"id":"a1","command":"autopilot","action":"start","mode":"slo-guard"}`)
	errs := eventsNamed(events, "error")
	if len(errs) != 1 || errs[0]["reason"] != "unknown_command" {
		t.Fatalf("errors = %v", errs)
	}
}

func TestHeadless_FindCeilingPreset_PinsStartRateAndOwnsSchedule(t *testing.T) {
	rb := newAPRunner()
	p := autopilot.Params{Mode: autopilot.ModeFindCeiling, StartRPS: 25, MaxRPS: 200}
	r := &HeadlessRenderer{
		Reporter: "json", ControlMode: "stdin", HeartbeatInterval: time.Hour, Autopilot: &p,
		ControlInput: strings.NewReader(`{"id":"a1","command":"autopilot","action":"stop"}` + "\n" +
			`{"id":"a2","command":"autopilot","action":"start","mode":"find-ceiling"}` + "\n" +
			`{"command":"stop"}` + "\n"),
	}
	out := captureStdout(t, func() {
		if err := r.Run(rb, testConfig(), nil, RunOptions{}); err != nil {
			t.Fatal(err)
		}
	})
	events := decodeEvents(t, out)
	for _, e := range eventsNamed(events, "error") {
		t.Errorf("a find-ceiling run may stop and restart its own controller: %v", e)
	}
	var starts []float64
	for _, e := range eventsNamed(events, "autopilot") {
		if e["state"] == "stepping" {
			starts = append(starts, e["hold_rps"].(float64))
		}
	}
	// The preset pins 25; the restart in the script has no start_rps, so it uses the default 20.
	if len(starts) != 2 || starts[0] != 25 || starts[1] != 20 || rb.GetHoldRPS() != 20 {
		t.Errorf("start holds = %v, final hold %d, want [25 20] and 20", starts, rb.GetHoldRPS())
	}
	if meta := r.AutopilotMeta(); meta == nil || meta.Mode != "find-ceiling" || meta.Outcome != "stopped" || len(meta.Events) < 4 {
		t.Errorf("meta = %+v (both controller stints should be kept)", meta)
	}
}

func TestHeadless_Autopilot_LiveGuardClampsMarksAndShowsOnHeartbeats(t *testing.T) {
	rb := newAPRunner()
	rb.haveW = true
	rb.window = engine.HealthWindow{Seconds: 5, P99Ms: 900, Samples: 500}
	p := autopilot.Params{Mode: autopilot.ModeSLOGuard, MaxP99Ms: 200}
	pr, pw := io.Pipe()
	r := &HeadlessRenderer{Reporter: "json", ControlMode: "stdin", ControlInput: pr, HeartbeatInterval: 500 * time.Millisecond, Autopilot: &p}
	go func() {
		time.Sleep(3500 * time.Millisecond)
		_, _ = pw.Write([]byte(`{"command":"stop"}` + "\n"))
	}()
	cfg := testConfig()
	cfg.Stages = []config.Stage{{Duration: 0, TargetRPS: 1000}, {Duration: time.Hour, TargetRPS: 1000}}
	out := captureStdout(t, func() {
		if err := r.Run(rb, cfg, nil, RunOptions{}); err != nil {
			t.Fatal(err)
		}
	})
	events := decodeEvents(t, out)

	var clamped bool
	for _, e := range eventsNamed(events, "autopilot") {
		if e["state"] == "clamped" && e["hold_rps"] == 70.0 {
			clamped = true
		}
	}
	if !clamped {
		t.Errorf("no clamp to 70 RPS in %v", eventsNamed(events, "autopilot"))
	}
	if rb.GetHoldRPS() != 70 {
		t.Errorf("engine hold = %d, want 70", rb.GetHoldRPS())
	}
	var marked bool
	for _, m := range eventsNamed(events, "mark") {
		if m["source"] == "autopilot" && strings.Contains(m["label"].(string), "SLO Guard triggered") {
			marked = true
		}
	}
	if !marked {
		t.Errorf("no autopilot mark in %v", eventsNamed(events, "mark"))
	}
	var shown bool
	for _, h := range eventsNamed(events, "heartbeat") {
		if st, ok := h["autopilot"].(map[string]any); ok && st["mode"] == "slo-guard" && st["state"] == "clamped" {
			shown = true
		}
	}
	if !shown {
		t.Error("no heartbeat carried the autopilot state")
	}
	if got := r.Marks(); len(got) == 0 || got[0].Source != "autopilot" {
		t.Errorf("recorded marks = %+v", got)
	}
}
