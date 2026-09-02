package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// fakeRunner is a minimal engine.Runner that returns immediately from
// RunStages, just enough to drive Run() through its "started" emission
// without any real HTTP traffic.
type fakeRunner struct{}

func (fakeRunner) RunStages(ctx context.Context, cfg *config.Config, specs []httpreader.RequestSpec) error {
	return nil
}

func (fakeRunner) Run(ctx context.Context, targetRPS int, duration time.Duration, specs []httpreader.RequestSpec) error {
	return nil
}

func (fakeRunner) IsRunning() bool                         { return false }
func (fakeRunner) GetStartTime() time.Time                 { return time.Time{} }
func (fakeRunner) GetEndTime() time.Time                   { return time.Time{} }
func (fakeRunner) GetElapsedTime() float64                 { return 0 }
func (fakeRunner) GetMetrics() *engine.MetricsSnapshot     { return &engine.MetricsSnapshot{} }
func (fakeRunner) GetRecentLogs(int) []engine.CallLog      { return nil }
func (fakeRunner) GetRecentErrorLogs(int) []engine.CallLog { return nil }
func (fakeRunner) ApplyBias(int)                           {}
func (fakeRunner) GetBias() int                            { return 0 }
func (fakeRunner) SetTargetRPS(int)                        {}

var _ engine.Runner = fakeRunner{}

// blockingRunner blocks in RunStages until ctx is canceled, mirroring a real
// engine mid-run. Needed whenever a test's control commands must all be
// processed before the run ends — a runner that returns immediately races
// the reader goroutine, since RunStages() and controlReader() both start via
// bare `go` with no ordering guarantee. ApplyBias/GetBias are real and
// stateful (fakeRunner's are both no-ops), for tests that check cumulative.
type blockingRunner struct {
	fakeRunner
	bias int
}

func (r *blockingRunner) RunStages(ctx context.Context, cfg *config.Config, specs []httpreader.RequestSpec) error {
	<-ctx.Done()
	return ctx.Err()
}
func (r *blockingRunner) ApplyBias(delta int) { r.bias += delta }
func (r *blockingRunner) GetBias() int        { return r.bias }
func (r *blockingRunner) GetStartTime() time.Time {
	return time.Now().Add(-time.Millisecond) // non-zero, so elapsedSince doesn't hit the zero-time guard
}

var _ engine.Runner = &blockingRunner{}

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. HeadlessRenderer.emit writes to os.Stdout
// directly, so this is the only way to see the raw marshaled bytes.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func testConfig() *config.Config {
	return &config.Config{
		ConfigSection: config.Section{HTTPFile: "test.http"},
		Stages:        []config.Stage{{Duration: time.Millisecond, TargetRPS: 1}},
	}
}

// TestHeadlessRenderer_StartedEvent_Capabilities is the 1.4 golden-JSON test:
// it asserts on the raw marshaled string, not just an unmarshal round-trip,
// because a round-trip can't distinguish nil from an explicitly-empty slice
// (the exact bug the *[]string pointer type in §2.4/0.2 exists to avoid).
func TestHeadlessRenderer_StartedEvent_Capabilities(t *testing.T) {
	tests := []struct {
		name        string
		controlMode string
		wantCaps    string
	}{
		{"default (unset) is stdin", "", `"capabilities":["control.bias","control.mark","control.stop"]`},
		{"explicit stdin", "stdin", `"capabilities":["control.bias","control.mark","control.stop"]`},
		{"control none", "none", `"capabilities":[]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// ControlInput is a hermetic empty reader so a "stdin" mode here
			// doesn't touch the test process's real os.Stdin.
			r := &HeadlessRenderer{Reporter: "json", ControlMode: tt.controlMode, ControlInput: strings.NewReader("")}

			out := captureStdout(t, func() {
				if err := r.Run(fakeRunner{}, testConfig(), nil, RunOptions{}); err != nil {
					t.Fatalf("Run: %v", err)
				}
			})

			started, _, _ := strings.Cut(out, "\n")
			if !strings.Contains(started, `"event":"started"`) {
				t.Fatalf("first line is not a started event: %s", started)
			}
			if !strings.Contains(started, `"protocol_version":1`) {
				t.Errorf("started event missing protocol_version:1, got: %s", started)
			}
			if !strings.Contains(started, tt.wantCaps) {
				t.Errorf("started event capabilities mismatch: want substring %q, got: %s", tt.wantCaps, started)
			}
			if !strings.Contains(started, `"bias":0`) {
				t.Errorf("started event missing bias:0 (should have no omitempty), got: %s", started)
			}
		})
	}
}

// TestHeadlessRenderer_GamedayScript is the 2.12 loop-level test: the scripted
// reader is fed testdata/gameday-script.jsonl (§3.10) — the single source of
// truth for this known-good sequence — and the resulting event stream must
// match its documented outcomes exactly, in order.
func TestHeadlessRenderer_GamedayScript(t *testing.T) {
	f, err := os.Open("testdata/gameday-script.jsonl")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	r := &HeadlessRenderer{
		Reporter:          "json",
		ControlMode:       "stdin",
		ControlInput:      f,
		HeartbeatInterval: time.Hour, // keep heartbeats out of the way
	}

	rb := &blockingRunner{}
	var runErr error
	out := captureStdout(t, func() {
		runErr = r.Run(rb, testConfig(), nil, RunOptions{})
	})
	if runErr != nil {
		t.Fatalf("Run returned an error (stop should exit 0): %v", runErr)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	var payloads []HeartbeatPayload
	for _, l := range lines {
		var p HeartbeatPayload
		if err := json.Unmarshal([]byte(l), &p); err != nil {
			t.Fatalf("line did not parse as JSON: %s: %v", l, err)
		}
		payloads = append(payloads, p)
	}

	// One event per fixture line, plus "started" at the front and "stopped"
	// tacked on the end by line 10's stop command (§3.10) — 12 total.
	wantEvents := []string{
		"started",
		"ack",     // 1: bias +5
		"mark",    // 2: mark gameday-start
		"error",   // 3: not even json → parse_error
		"ack",     // 4: bias -3
		"error",   // 5: bias amount 0 → invalid_argument
		"error",   // 6: pause → unknown_command
		"error",   // 7: mark label "" → invalid_argument
		"mark",    // 8: mark, no id
		"ack",     // 9: bias +20
		"ack",     // 10: stop's own ack
		"stopped", // 10: stop's terminal event
	}
	if len(payloads) != len(wantEvents) {
		t.Fatalf("got %d events, want %d\nevents: %+v", len(payloads), len(wantEvents), eventNames(payloads))
	}
	for i, want := range wantEvents {
		if payloads[i].Event != want {
			t.Errorf("event[%d] = %q, want %q (full sequence: %v)", i, payloads[i].Event, want, eventNames(payloads))
		}
	}

	// Spot-check the fields that matter, not just the event names.
	line3 := payloads[3] // "not even json" → parse_error
	if line3.Reason != "parse_error" || line3.ID != "" || line3.Command != "" {
		t.Errorf("parse_error line should have no id/command: %+v", line3)
	}
	line6 := payloads[6] // pause → unknown_command
	if line6.Reason != "unknown_command" || line6.ID != "c6" || line6.Command != "pause" {
		t.Errorf("unknown_command line wrong: %+v", line6)
	}
	line8 := payloads[8] // mark, no id in the source line
	if line8.Event != "mark" || line8.ID != "" || line8.Label != "deploy-v2-canary" {
		t.Errorf("id-less mark line wrong: %+v", line8)
	}
	line9 := payloads[9]  // bias +20 ack
	if line9.Bias != 22 { // 5 - 3 + 20
		t.Errorf("final bias ack cumulative = %d, want 22", line9.Bias)
	}
	stopped := payloads[11]
	if stopped.ID != "" || stopped.Command != "" {
		t.Errorf("stopped is a terminal event, not a reply — should carry no id/command: %+v", stopped)
	}

	if rb.GetBias() != 22 {
		t.Errorf("engine's final cumulative bias = %d, want 22", rb.GetBias())
	}
	if len(r.Marks()) != 2 {
		t.Errorf("want 2 marks recorded, got %d: %+v", len(r.Marks()), r.Marks())
	}
	if len(r.BiasEvents()) != 3 {
		t.Errorf("want 3 bias events recorded (amount=0 must not record one), got %d: %+v", len(r.BiasEvents()), r.BiasEvents())
	}
}

func eventNames(payloads []HeartbeatPayload) []string {
	out := make([]string, len(payloads))
	for i, p := range payloads {
		out[i] = p.Event
	}
	return out
}

// TestHeadlessRenderer_Stop_SingleTerminalEvent confirms §3.3: a stop-ended
// run produces exactly one terminal event (stopped, never stopped+finished),
// and Run() returns nil (exit 0).
func TestHeadlessRenderer_Stop_SingleTerminalEvent(t *testing.T) {
	r := &HeadlessRenderer{
		Reporter:          "json",
		ControlMode:       "stdin",
		ControlInput:      strings.NewReader(`{"id":"c1","command":"stop"}` + "\n"),
		HeartbeatInterval: time.Hour,
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = r.Run(&blockingRunner{}, testConfig(), nil, RunOptions{})
	})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if n := strings.Count(out, `"event":"stopped"`); n != 1 {
		t.Errorf("want exactly 1 stopped event, got %d:\n%s", n, out)
	}
	if strings.Contains(out, `"event":"finished"`) {
		t.Errorf("stop must not also emit finished:\n%s", out)
	}
}

// TestHeadlessRenderer_Stop_SnapshotHasMarksAndBiasEvents runs the full
// pipeline a real cmd/gg would: OnRunComplete finalizes a real
// snap.DefaultRecorder using hr.Marks()/hr.BiasEvents(), exactly as main.go's
// finalizeSnapResult does. Confirms marks/bias_events/final_bias actually
// reach the resulting Snapshot after a stop-ended run.
func TestHeadlessRenderer_Stop_SnapshotHasMarksAndBiasEvents(t *testing.T) {
	script := strings.NewReader(
		`{"id":"c1","command":"bias","amount":5}` + "\n" +
			`{"id":"c2","command":"mark","label":"canary"}` + "\n" +
			`{"id":"c3","command":"stop"}` + "\n",
	)
	r := &HeadlessRenderer{
		Reporter:          "json",
		ControlMode:       "stdin",
		ControlInput:      script,
		HeartbeatInterval: time.Hour,
	}
	rb := &blockingRunner{}

	rec := snap.NewDefaultRecorder(0)
	var snapshot *snap.Snapshot
	onRunComplete := func() string {
		s, err := rec.Finalize(snap.RunMeta{
			Marks:      r.Marks(),
			BiasEvents: r.BiasEvents(),
			FinalBias:  rb.GetBias(),
		})
		if err != nil {
			t.Errorf("Finalize: %v", err)
			return ""
		}
		snapshot = s
		return "done"
	}

	var runErr error
	captureStdout(t, func() {
		runErr = r.Run(rb, testConfig(), nil, RunOptions{OnRunComplete: onRunComplete})
	})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}

	if snapshot == nil {
		t.Fatal("OnRunComplete never finalized a snapshot")
	}
	if len(snapshot.Meta.Marks) != 1 || snapshot.Meta.Marks[0].Label != "canary" {
		t.Errorf("Meta.Marks = %+v", snapshot.Meta.Marks)
	}
	if len(snapshot.Meta.BiasEvents) != 1 || snapshot.Meta.BiasEvents[0].Amount != 5 {
		t.Errorf("Meta.BiasEvents = %+v", snapshot.Meta.BiasEvents)
	}
	if snapshot.Meta.FinalBias != 5 {
		t.Errorf("Meta.FinalBias = %d, want 5", snapshot.Meta.FinalBias)
	}
}

// TestHeadlessRenderer_TextReporter_Unaffected confirms LCP's new fields are
// JSON-only: the human-readable reporter still prints just "[time] message".
func TestHeadlessRenderer_TextReporter_Unaffected(t *testing.T) {
	r := &HeadlessRenderer{Reporter: "text", ControlMode: "none"}

	out := captureStdout(t, func() {
		if err := r.Run(fakeRunner{}, testConfig(), nil, RunOptions{}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	})

	started, _, _ := strings.Cut(out, "\n")
	if strings.Contains(started, "protocol_version") || strings.Contains(started, "capabilities") {
		t.Errorf("text reporter line should not leak LCP field names, got: %s", started)
	}
	if !strings.HasPrefix(started, "[") || !strings.Contains(started, "Load test started") {
		t.Errorf("text reporter started line has unexpected shape: %s", started)
	}
}
