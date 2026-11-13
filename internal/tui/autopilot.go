package tui

import (
	"fmt"

	"github.com/shyam-s00/gopher-glide/internal/autopilot"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// Autopilot attaches the built-in controller to a TUI run. Marks and Result
// are filled in as the run goes and are complete once Start returns, or once
// the post-run callback fires.
type Autopilot struct {
	Params autopilot.Params
	Marks  []snap.Mark
	Result *autopilot.Result

	host   *autopilot.Host
	status string
}

// active reports whether the controller still owns the rate. Safe on nil.
func (a *Autopilot) active() bool { return a != nil && a.host.Active() }

// poll applies whatever the controller has said since the last call and
// reports whether it asked for the run to end.
func (a *Autopilot) poll(eng engine.Runner) (endRun bool) {
	if a == nil || a.host == nil {
		return false
	}
	for {
		select {
		case m := <-a.host.In():
			if a.apply(eng, m) {
				endRun = true
			}
		default:
			return endRun
		}
	}
}

func (a *Autopilot) apply(eng engine.Runner, m autopilot.Msg) (endRun bool) {
	switch m.Kind {
	case autopilot.MsgMark:
		a.Marks = append(a.Marks, snap.Mark{Label: m.Label, ElapsedS: elapsedSince(eng.GetStartTime()), Source: "autopilot"})
	case autopilot.MsgEvent:
		a.status = fmt.Sprintf("🤖 %s · %s", m.Event.Mode, m.Event.State)
		if m.Event.HoldRPS > 0 {
			a.status += fmt.Sprintf(" · %d RPS", m.Event.HoldRPS)
		}
		if m.Event.State == autopilot.StateDone {
			a.status += fmt.Sprintf(" · %s", m.Event.Outcome)
			if m.Event.CeilingRPS > 0 {
				a.status += fmt.Sprintf(" %d RPS", m.Event.CeilingRPS)
			}
		}
	case autopilot.MsgStop:
		return true
	}
	return false
}

// finish stops the controller and records its result. It is safe to call more
// than once; runEnded is true when the run ended on its own, not by a quit.
func (a *Autopilot) finish(eng engine.Runner, runEnded bool) {
	if a == nil || a.host == nil {
		return
	}
	res, rest := a.host.Halt(runEnded)
	for _, m := range rest {
		a.apply(eng, m)
	}
	a.Result = &res
	a.host = nil
}
