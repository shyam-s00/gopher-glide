package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// UserLockedError is returned when an agent touches a user's run that the
// user has not opened to agent steering.
type UserLockedError struct{ ID string }

func (e *UserLockedError) Error() string {
	return fmt.Sprintf("user_session_locked: %s was started by the user and is read-only for agents; "+
		"get_live_telemetry still works, and the user can allow steering from the run bar", e.ID)
}

// Origin returns who started the session.
func (s *Session) Origin() Origin { return s.origin }

// AgentSteerAllowed reports whether an agent may steer or stop this session.
// Agent sessions are always open; a user session only when the user says so.
func (s *Session) AgentSteerAllowed() bool {
	if s.origin != OriginUser {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentSteer
}

// AgentAccess returns a UserLockedError unless an agent may act on the session.
func (s *Session) AgentAccess() error {
	if s.AgentSteerAllowed() {
		return nil
	}
	return &UserLockedError{ID: s.ID}
}

// SetAgentSteer opens or closes a user session to agent steering and tells
// event subscribers, so a run bar can show the state.
func (s *Session) SetAgentSteer(allow bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentSteer == allow {
		return
	}
	s.agentSteer = allow
	ev := s.agentSteerEventLocked()
	for sub := range s.plug {
		sub.push(ev)
	}
}

func (s *Session) agentSteerEventLocked() Event {
	raw, _ := json.Marshal(map[string]any{"event": "ack", "command": "agent_steer", "allow": s.agentSteer})
	return s.envelopeLocked("ack", raw)
}

func (s *Session) checkAgentRunning() error {
	if err := s.AgentAccess(); err != nil {
		return err
	}
	return s.checkRunning()
}

// userCommands are the LCP commands a plugin may send through Command.
var userCommands = map[string]bool{"bias": true, "hold": true, "release": true, "mark": true, "stop": true, "autopilot": true}

// stampsSource lists the commands that carry a source tag.
var stampsSource = map[string]bool{"bias": true, "hold": true, "release": true, "mark": true}

// ErrUnknownCommand is returned for a command the plugin may not send.
var ErrUnknownCommand = errors.New("unknown command")

// Command sends one LCP command on the user's behalf and returns gg's reply
// event. A reply that is an error event is still a reply: it is returned, not
// raised. The reply is gg's own, except that its id is the caller's.
func (s *Session) Command(ctx context.Context, cmd map[string]any, source string) (json.RawMessage, error) {
	name, _ := cmd["command"].(string)
	if !userCommands[name] {
		return nil, fmt.Errorf("%w %q", ErrUnknownCommand, name)
	}
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if err := s.checkRunning(); err != nil {
		return nil, err
	}
	callerID, hadID := cmd["id"]
	if stampsSource[name] {
		cmd["source"] = source
	}
	r, err := s.request(ctx, cmd)
	var re *ReplyError
	if err != nil && !errors.As(err, &re) {
		return nil, err
	}
	return withID(r.Raw, callerID, hadID), nil
}

// withID swaps the id of a reply for the caller's, or removes it when the caller sent none.
func withID(raw json.RawMessage, id any, had bool) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	if had {
		b, _ := json.Marshal(id)
		m["id"] = b
	} else {
		delete(m, "id")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}
