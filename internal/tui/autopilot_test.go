package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shyam-s00/gopher-glide/internal/autopilot"
)

func newAutopilotModel(t *testing.T) (model, *Autopilot) {
	t.Helper()
	m := newTestModel()
	ap := &Autopilot{Params: autopilot.Params{Mode: autopilot.ModeSLOGuard}}
	host, err := autopilot.StartHost(m.engine, m.config, ap.Params)
	if err != nil {
		t.Fatalf("StartHost: %v", err)
	}
	ap.host = host
	m.ap = ap
	t.Cleanup(func() { ap.finish(m.engine, false) })
	return m, ap
}

func TestAutopilot_ArrowKeysIgnoredWhileItOwnsTheRate(t *testing.T) {
	m, _ := newAutopilotModel(t)
	m.running = true
	for _, key := range []tea.KeyType{tea.KeyUp, tea.KeyDown} {
		m2, _ := m.Update(tea.KeyMsg{Type: key})
		if got := m2.(model).directorMsg; !strings.Contains(got, "autopilot") {
			t.Errorf("director message = %q, want the autopilot notice", got)
		}
	}
	if bias := m.engine.GetBias(); bias != 0 {
		t.Errorf("bias = %d, want 0", bias)
	}
}

func TestAutopilot_ArrowKeysWorkOnceItIsDone(t *testing.T) {
	m, ap := newAutopilotModel(t)
	m.running = true
	ap.finish(m.engine, false)
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if bias := m.engine.GetBias(); bias != 5 {
		t.Errorf("bias = %d, want 5 after the autopilot ended", bias)
	}
}

func TestAutopilot_PollRecordsMarksStatusAndStop(t *testing.T) {
	m, ap := newAutopilotModel(t)
	if ap.poll(m.engine) {
		t.Fatal("poll asked to end the run with nothing to report")
	}
	if !strings.Contains(ap.status, "slo-guard") || !strings.Contains(ap.status, "watching") {
		t.Errorf("status = %q, want the initial watching state", ap.status)
	}

	ap.apply(m.engine, autopilot.Msg{Kind: autopilot.MsgMark, Label: "clamped"})
	if len(ap.Marks) != 1 || ap.Marks[0].Source != "autopilot" || ap.Marks[0].Label != "clamped" {
		t.Errorf("marks = %+v", ap.Marks)
	}
	if !ap.apply(m.engine, autopilot.Msg{Kind: autopilot.MsgStop}) {
		t.Error("a stop message must end the run")
	}
	ap.apply(m.engine, autopilot.Msg{Kind: autopilot.MsgEvent, Event: autopilot.Event{
		Mode: autopilot.ModeFindCeiling, State: autopilot.StateDone, Outcome: autopilot.OutcomeCeilingFound, CeilingRPS: 90,
	}})
	if !strings.Contains(ap.status, "ceiling_found 90 RPS") {
		t.Errorf("status = %q", ap.status)
	}
}

func TestAutopilot_FinishRecordsResultOnceAndNilIsSafe(t *testing.T) {
	m, ap := newAutopilotModel(t)
	ap.finish(m.engine, true)
	first := ap.Result
	if first == nil || first.Outcome != autopilot.OutcomeCompleted {
		t.Fatalf("result = %+v, want completed for a guard whose run ended", first)
	}
	ap.finish(m.engine, false)
	if ap.Result != first {
		t.Error("a second finish must not replace the result")
	}

	var none *Autopilot
	none.finish(m.engine, true)
	if none.poll(m.engine) || none.active() {
		t.Error("nil autopilot must be inert")
	}
}
