package session

import (
	"context"
	"errors"

	"github.com/shyam-s00/gopher-glide/internal/ui"
)

// AutopilotInfo is the built-in autopilot's state as gg last reported it.
type AutopilotInfo struct {
	Mode       string `json:"mode"`
	State      string `json:"state"`
	HoldRPS    int    `json:"hold_rps,omitempty"`
	Reason     string `json:"reason,omitempty"`
	CeilingRPS int    `json:"ceiling_rps,omitempty"`
	// Outcome is set once the autopilot has finished.
	Outcome string `json:"outcome,omitempty"`
}

const stateDone = "done"

// autopilotActive reports whether a controller still owns the rate; the caller holds s.mu.
func (s *Session) autopilotActive() bool {
	return s.ap != nil && s.ap.State != stateDone
}

// AutopilotActive reports whether a built-in controller currently owns the rate.
func (s *Session) AutopilotActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autopilotActive()
}

// noteAutopilotEvent folds an "autopilot" event into the session; the caller holds s.mu.
func (s *Session) noteAutopilotEvent(ev *ui.HeartbeatPayload) {
	info := &AutopilotInfo{Mode: ev.Mode, State: ev.State, Reason: clip(ev.Reason, maxMessageBytes), Outcome: ev.Outcome}
	if ev.HoldRPS != nil {
		info.HoldRPS = *ev.HoldRPS
	}
	if ev.CeilingRPS != nil {
		info.CeilingRPS = *ev.CeilingRPS
	}
	s.ap = info
}

// noteAutopilotBeat keeps the state current from a heartbeat's compact status;
// the caller holds s.mu.
func (s *Session) noteAutopilotBeat(ev *ui.HeartbeatPayload) {
	if ev.Autopilot == nil {
		if s.ap != nil && s.ap.State != stateDone {
			s.ap = nil
		}
		return
	}
	if s.ap == nil || s.ap.Mode != ev.Autopilot.Mode {
		s.ap = &AutopilotInfo{Mode: ev.Autopilot.Mode}
	}
	s.ap.State = ev.Autopilot.State
}

// noteAutopilotFinal records the outcome from the terminal event; the caller holds s.mu.
func (s *Session) noteAutopilotFinal(ev *ui.HeartbeatPayload) {
	if ev.Mode == "" {
		return
	}
	if s.ap == nil {
		s.ap = &AutopilotInfo{Mode: ev.Mode}
	}
	s.ap.State, s.ap.Outcome = stateDone, ev.Outcome
	if ev.CeilingRPS != nil {
		s.ap.CeilingRPS = *ev.CeilingRPS
	}
}

// ErrNoAutopilot is returned when gg cannot run an autopilot.
var ErrNoAutopilot = errors.New("this gg build has no built-in autopilot (it does not advertise control.autopilot); steer with adjust_rps, set_rps or hold_rps")

// AutopilotArgs are the settings for starting a controller on a running simulation.
type AutopilotArgs struct {
	Mode         string
	MaxP99Ms     float64
	MaxErrorRate float64
}

// StartAutopilot asks gg to start a controller on the running simulation.
func (s *Session) StartAutopilot(ctx context.Context, a AutopilotArgs) error {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if err := s.checkAgentRunning(); err != nil {
		return err
	}
	if !s.hasCapability("control.autopilot") {
		return ErrNoAutopilot
	}
	cmd := map[string]any{"command": "autopilot", "action": "start", "mode": a.Mode}
	if a.MaxP99Ms > 0 {
		cmd["max_p99_ms"] = a.MaxP99Ms
	}
	if a.MaxErrorRate > 0 {
		cmd["max_error_rate"] = a.MaxErrorRate
	}
	if _, err := s.request(ctx, cmd); err != nil {
		return err
	}
	s.recordSteer(SteerInfo{Action: "autopilot_start", Mode: a.Mode})
	return nil
}

// StopAutopilot stops the controller; the rate stays where it was until the caller changes it.
func (s *Session) StopAutopilot(ctx context.Context) error {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if err := s.checkAgentRunning(); err != nil {
		return err
	}
	if !s.hasCapability("control.autopilot") {
		return ErrNoAutopilot
	}
	if _, err := s.request(ctx, map[string]any{"command": "autopilot", "action": "stop"}); err != nil {
		return err
	}
	s.recordSteer(SteerInfo{Action: "autopilot_stop"})
	return nil
}
