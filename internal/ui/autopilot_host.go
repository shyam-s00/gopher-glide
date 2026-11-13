package ui

import (
	"encoding/json"
	"fmt"

	"github.com/shyam-s00/gopher-glide/internal/autopilot"
	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// AutopilotStatus is the compact controller state carried on heartbeats.
type AutopilotStatus struct {
	Mode  string `json:"mode"`
	State string `json:"state"`
}

// apStatus is the compact controller state for a heartbeat, nil when none runs.
func apStatus(h *autopilot.Host) *AutopilotStatus {
	mode, state, ok := h.Status()
	if !ok {
		return nil
	}
	return &AutopilotStatus{Mode: string(mode), State: string(state)}
}

// autopilotMeta builds the snapshot record from a finished controller.
func autopilotMeta(res *autopilot.Result, prior []autopilot.Event) *snap.AutopilotMeta {
	if res == nil {
		return nil
	}
	params, _ := json.Marshal(res.Params)
	meta := &snap.AutopilotMeta{Mode: string(res.Mode), Outcome: string(res.Outcome), CeilingRPS: res.CeilingRPS, Params: params}
	for _, e := range append(append([]autopilot.Event(nil), prior...), res.Events...) {
		meta.Events = append(meta.Events, snap.AutopilotEvent{
			State: string(e.State), HoldRPS: e.HoldRPS, Reason: e.Reason,
			CeilingRPS: e.CeilingRPS, Outcome: string(e.Outcome), ElapsedS: e.ElapsedS,
		})
	}
	return meta
}

// autopilotPayload renders a controller event as an LCP "autopilot" event.
func autopilotPayload(e autopilot.Event) HeartbeatPayload {
	el := e.ElapsedS
	p := HeartbeatPayload{
		Time: now(), Event: "autopilot", Mode: string(e.Mode), State: string(e.State),
		Reason: e.Reason, Outcome: string(e.Outcome), ElapsedS: &el,
		Message: fmt.Sprintf("autopilot %s: %s", e.State, e.Reason),
	}
	if e.HoldRPS > 0 {
		h := e.HoldRPS
		p.HoldRPS = &h
	}
	if e.CeilingRPS > 0 {
		c := e.CeilingRPS
		p.CeilingRPS = &c
	}
	return p
}

// handleAutopilotCommand executes an LCP "autopilot" start or stop.
func (r *HeadlessRenderer) handleAutopilotCommand(eng engine.Runner, cfg *config.Config, cmd controlCommand) {
	reject := func(reason, msg string) {
		r.emit(HeartbeatPayload{Time: now(), Event: "error", ID: cmd.ID, Command: cmd.Command, Reason: reason, Message: msg})
	}
	if _, ok := eng.(autopilot.Target); !ok {
		r.emitUnsupported(cmd)
		return
	}
	ac := cmd.Autopilot
	if ac.Action == "stop" {
		if !r.ap.Active() {
			reject("invalid_argument", "no autopilot is running")
			return
		}
		r.finishAutopilot(eng, false)
		r.emit(HeartbeatPayload{Time: now(), Event: "ack", ID: cmd.ID, Command: cmd.Command, ElapsedS: runElapsed(eng),
			Message: "autopilot stopped; the current hold stays until released"})
		return
	}
	if r.ap.Active() {
		reject("autopilot_active", "an autopilot is already running")
		return
	}
	if ac.Params.Mode == autopilot.ModeFindCeiling && (r.Autopilot == nil || r.Autopilot.Mode != autopilot.ModeFindCeiling) {
		reject("invalid_argument", "find-ceiling owns the run schedule; start the run with --autopilot find-ceiling")
		return
	}
	if r.ap != nil {
		r.finishAutopilot(eng, false)
	}
	run, err := autopilot.StartHost(eng, cfg, ac.Params)
	if err != nil {
		reject("invalid_argument", err.Error())
		return
	}
	r.ap = run
	r.emit(HeartbeatPayload{Time: now(), Event: "ack", ID: cmd.ID, Command: cmd.Command, Mode: string(ac.Params.Mode),
		ElapsedS: runElapsed(eng), Message: fmt.Sprintf("autopilot %s started", ac.Params.Mode)})
}

// handleAutopilotMsg records and prints one controller message. It reports
// true when the controller asked for the run to end.
func (r *HeadlessRenderer) handleAutopilotMsg(eng engine.Runner, m autopilot.Msg) (endRun bool) {
	switch m.Kind {
	case autopilot.MsgMark:
		elapsed := elapsedSince(eng.GetStartTime())
		r.marks = append(r.marks, snap.Mark{Label: m.Label, ElapsedS: elapsed, Source: "autopilot"})
		r.emit(HeartbeatPayload{Time: now(), Event: "mark", Label: m.Label, ElapsedS: &elapsed, Source: "autopilot"})
	case autopilot.MsgEvent:
		r.emit(autopilotPayload(m.Event))
	case autopilot.MsgStop:
		return true
	}
	return false
}

// finishAutopilot stops the controller and keeps its result.
func (r *HeadlessRenderer) finishAutopilot(eng engine.Runner, runEnded bool) {
	res, rest := r.ap.Halt(runEnded)
	for _, m := range rest {
		r.handleAutopilotMsg(eng, m)
	}
	if res.Closing != nil {
		r.emit(autopilotPayload(*res.Closing))
	}
	if r.apResult != nil {
		r.apPrior = append(r.apPrior, r.apResult.Events...)
	}
	r.apResult = &res
	r.ap = nil
}

// endAutopilot closes out any controller still attached when the run ends.
func (r *HeadlessRenderer) endAutopilot(eng engine.Runner, runEnded bool) {
	if r.ap != nil {
		r.finishAutopilot(eng, runEnded)
	}
}

func apMode(res *autopilot.Result) string {
	if res == nil {
		return ""
	}
	return string(res.Mode)
}

func apOutcome(res *autopilot.Result) string {
	if res == nil {
		return ""
	}
	return string(res.Outcome)
}

func apCeiling(res *autopilot.Result) *int {
	if res == nil || res.CeilingRPS == 0 {
		return nil
	}
	c := res.CeilingRPS
	return &c
}
