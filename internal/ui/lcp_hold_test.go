package ui

import (
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// holdRunner is a blocking runner that supports hold and reports windowed health.
type holdRunner struct {
	fixedStartRunner
	hold   int
	window engine.HealthWindow
	haveW  bool
}

func (r *holdRunner) HoldRPS(rps int) { r.hold = rps }
func (r *holdRunner) ReleaseHold()    { r.hold = 0 }
func (r *holdRunner) GetHoldRPS() int { return r.hold }
func (r *holdRunner) IsRunning() bool { return true }
func (r *holdRunner) HealthSince(time.Duration) (engine.HealthWindow, bool) {
	return r.window, r.haveW
}

var (
	_ engine.Holder         = &holdRunner{}
	_ engine.WindowedHealth = &holdRunner{}
)

func TestHeadless_HoldCapability_OnlyWhenEngineCanHold(t *testing.T) {
	for _, tt := range []struct {
		name string
		eng  engine.Runner
		want bool
	}{
		{"holder", &holdRunner{}, true},
		{"plain runner", &blockingRunner{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &HeadlessRenderer{ControlMode: "stdin"}
			caps := *r.capabilities(tt.eng)
			has := false
			for _, c := range caps {
				if c == "control.hold" {
					has = true
				}
			}
			if has != tt.want {
				t.Errorf("control.hold advertised = %v, want %v (caps %v)", has, tt.want, caps)
			}
		})
	}
	r := &HeadlessRenderer{ControlMode: "none"}
	if caps := *r.capabilities(&holdRunner{}); len(caps) != 0 {
		t.Errorf("control none must advertise nothing, got %v", caps)
	}
}

func TestHeadless_HoldRelease_AckShape(t *testing.T) {
	rb := &holdRunner{fixedStartRunner: fixedStartRunner{start: time.Now().Add(-7 * time.Second)}}
	events, _ := runControlScript(t, rb,
		`{"id":"h1","command":"hold","rps":220,"source":"autopilot"}`,
		`{"id":"h2","command":"release","source":"user"}`,
		`{"id":"h3","command":"hold","rps":0}`,
	)
	acks := eventsNamed(events, "ack")
	if len(acks) < 3 { // hold, release, stop
		t.Fatalf("acks = %v", acks)
	}
	hold, release := acks[0], acks[1]
	if hold["id"] != "h1" || hold["command"] != "hold" || hold["hold_rps"] != 220.0 || hold["source"] != "autopilot" {
		t.Errorf("hold ack = %v", hold)
	}
	if el, _ := hold["elapsed_s"].(float64); el < 6.9 || el > 9 {
		t.Errorf("hold ack elapsed_s = %v", hold["elapsed_s"])
	}
	if _, has := release["hold_rps"]; has || release["command"] != "release" || release["source"] != "user" {
		t.Errorf("release ack = %v", release)
	}
	errs := eventsNamed(events, "error")
	if len(errs) != 1 || errs[0]["id"] != "h3" || errs[0]["reason"] != "invalid_argument" {
		t.Errorf("errors = %v", errs)
	}
	if rb.hold != 0 {
		t.Errorf("hold left active after release: %d", rb.hold)
	}
}

func TestHeadless_Hold_RejectedWhenEngineCannotHold(t *testing.T) {
	events, _ := runControlScript(t, &blockingRunner{}, `{"id":"h1","command":"hold","rps":10}`, `{"id":"h2","command":"release"}`)
	errs := eventsNamed(events, "error")
	if len(errs) != 2 || errs[0]["reason"] != "unknown_command" || errs[1]["reason"] != "unknown_command" {
		t.Fatalf("errors = %v", errs)
	}
}

func TestHeadless_SourceEchoedAndPersisted(t *testing.T) {
	rb := &holdRunner{fixedStartRunner: fixedStartRunner{start: time.Now().Add(-5 * time.Second)}}
	events, r := runControlScript(t, rb,
		`{"id":"b1","command":"bias","amount":4,"source":"agent"}`,
		`{"id":"m1","command":"mark","label":"from agent","source":"agent"}`,
		`{"id":"m2","command":"mark","label":"unattributed"}`,
		`{"id":"b2","command":"bias","amount":-1,"source":"robot"}`,
	)
	if ack := eventsNamed(events, "ack")[0]; ack["source"] != "agent" {
		t.Errorf("bias ack source = %v", ack["source"])
	}
	marks := eventsNamed(events, "mark")
	if marks[0]["source"] != "agent" {
		t.Errorf("mark source = %v", marks[0]["source"])
	}
	if _, has := marks[1]["source"]; has {
		t.Errorf("unattributed mark must omit source: %v", marks[1])
	}
	if errs := eventsNamed(events, "error"); len(errs) != 1 || errs[0]["reason"] != "invalid_argument" {
		t.Errorf("bad source should be invalid_argument: %v", errs)
	}

	s, err := snap.NewDefaultRecorder(0).Finalize(snap.RunMeta{Marks: r.Marks(), BiasEvents: r.BiasEvents()})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Meta.BiasEvents) != 1 || s.Meta.BiasEvents[0].Source != "agent" {
		t.Errorf("bias events = %+v", s.Meta.BiasEvents)
	}
	if len(s.Meta.Marks) != 2 || s.Meta.Marks[0].Source != "agent" || s.Meta.Marks[1].Source != "" {
		t.Errorf("marks = %+v", s.Meta.Marks)
	}
}

func TestHeartbeat_CarriesHoldWindowAndElapsed(t *testing.T) {
	rb := &holdRunner{
		fixedStartRunner: fixedStartRunner{start: time.Now().Add(-3 * time.Second)},
		hold:             140,
		window:           engine.HealthWindow{Seconds: 5, P99Ms: 212.4, ErrorRate: 0.004, Samples: 1180},
		haveW:            true,
	}
	events, _ := runHeartbeats(t, rb)
	beats := eventsNamed(events, "heartbeat")
	if len(beats) == 0 {
		t.Fatal("no heartbeats")
	}
	for _, hb := range beats {
		w, ok := hb["window"].(map[string]any)
		if !ok || w["seconds"] != 5.0 || w["p99_ms"] != 212.4 || w["error_rate"] != 0.004 || w["samples"] != 1180.0 {
			t.Fatalf("window = %v", hb["window"])
		}
		if hb["hold_rps"] != 140.0 {
			t.Errorf("hold_rps = %v", hb["hold_rps"])
		}
		if el, _ := hb["elapsed_s"].(float64); el < 2.9 {
			t.Errorf("elapsed_s = %v", hb["elapsed_s"])
		}
	}
}

func TestHeartbeat_OmitsHoldAndWindowWhenNotAvailable(t *testing.T) {
	rb := &holdRunner{fixedStartRunner: fixedStartRunner{start: time.Now()}} // no hold, no window yet
	events, _ := runHeartbeats(t, rb)
	for _, hb := range eventsNamed(events, "heartbeat") {
		if _, has := hb["hold_rps"]; has {
			t.Errorf("hold_rps present without a hold: %v", hb)
		}
		if _, has := hb["window"]; has {
			t.Errorf("window present before the first reading: %v", hb)
		}
	}
	events, _ = runHeartbeats(t, &runningRunner{})
	for _, hb := range eventsNamed(events, "heartbeat") {
		if _, has := hb["window"]; has {
			t.Errorf("window present for an engine without health windows: %v", hb)
		}
	}
}

func TestHeadless_ElapsedZeroIsPreserved(t *testing.T) {
	events, _ := runControlScript(t, &fixedStartRunner{}, `{"id":"b1","command":"bias","amount":1}`)
	for _, e := range events {
		if e["event"] == "ack" || e["event"] == "stopped" {
			if v, has := e["elapsed_s"]; !has || v != 0.0 {
				t.Errorf("%v: elapsed_s = %v (present %v), want 0", e["event"], v, has)
			}
		}
	}
}

func TestHeadless_FinalElapsedUsesFrozenEnd(t *testing.T) {
	start := time.Now().Add(-30 * time.Second)
	rb := &endedRunner{fixedStartRunner: fixedStartRunner{start: start}, end: start.Add(12 * time.Second)}
	events, _ := runControlScript(t, rb)
	var stopped map[string]any
	for _, e := range events {
		if e["event"] == "stopped" {
			stopped = e
		}
	}
	if stopped == nil || stopped["elapsed_s"] != 12.0 {
		t.Errorf("stopped = %v, want elapsed_s 12", stopped)
	}
}

type endedRunner struct {
	fixedStartRunner
	end time.Time
}

func (r *endedRunner) GetEndTime() time.Time { return r.end }
