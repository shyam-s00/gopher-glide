package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// Origin says who started a session.
type Origin string

// Session origins. A user run comes from the IDE plugin, an agent run from an MCP tool.
const (
	OriginUser  Origin = "user"
	OriginAgent Origin = "agent"
)

// Subscription buffering: past subBuffer queued events a slow reader loses its
// oldest heartbeats; past subHardCap (only unlosable events remain) it is cut off.
const (
	subBuffer  = 256
	subHardCap = 4096
	maxReplay  = 50
)

// ErrSlowSubscriber ends a subscription whose reader stopped consuming.
var ErrSlowSubscriber = errors.New("event subscriber too slow; reconnect to resume from the current state")

// Envelope wraps one LCP event with the session it belongs to.
type Envelope struct {
	SessionID string `json:"session_id"`
	Origin    Origin `json:"origin"`
	// Autopilot is the built-in controller's mode while one runs.
	Autopilot string `json:"autopilot,omitempty"`
	// LCP is the event exactly as gg emitted it.
	LCP json.RawMessage `json:"lcp"`
}

// Event is one item of a subscription: an envelope, or a notice that
// heartbeats were dropped.
type Event struct {
	// Name is the LCP event name, or "lagged".
	Name     string
	Envelope *Envelope
	Dropped  int
}

// JSON renders the event as the data of one stream message.
func (e Event) JSON() []byte {
	if e.Envelope == nil {
		b, _ := json.Marshal(map[string]any{"event": "lagged", "dropped": e.Dropped})
		return b
	}
	b, _ := json.Marshal(e.Envelope)
	return b
}

// Subscription is one reader of every session's events.
type Subscription struct {
	m *Manager

	mu      sync.Mutex
	queue   []Event
	dropped int
	closed  bool
	err     error
	wake    chan struct{}
}

func newSubscription(m *Manager) *Subscription {
	return &Subscription{m: m, wake: make(chan struct{}, 1)}
}

// push queues an event without blocking. Heartbeats are the only events it
// will drop; the caller holds the owning session's lock.
func (sub *Subscription) push(ev Event) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed {
		return
	}
	if len(sub.queue) >= subBuffer {
		if i := oldestHeartbeat(sub.queue); i >= 0 {
			sub.queue = append(sub.queue[:i], sub.queue[i+1:]...)
			sub.dropped++
		} else if ev.Name == "heartbeat" {
			sub.dropped++
			return
		} else if len(sub.queue) >= subHardCap {
			sub.closed, sub.err = true, ErrSlowSubscriber
			sub.signal()
			return
		}
	}
	sub.queue = append(sub.queue, ev)
	sub.signal()
}

func oldestHeartbeat(q []Event) int {
	for i, ev := range q {
		if ev.Name == "heartbeat" {
			return i
		}
	}
	return -1
}

func (sub *Subscription) signal() {
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

// Next returns the next event, a lagged notice first if heartbeats were
// dropped. It returns io.EOF once the subscription is closed and drained.
func (sub *Subscription) Next(ctx context.Context) (Event, error) {
	for {
		sub.mu.Lock()
		switch {
		case sub.dropped > 0:
			ev := Event{Name: "lagged", Dropped: sub.dropped}
			sub.dropped = 0
			sub.mu.Unlock()
			return ev, nil
		case len(sub.queue) > 0:
			ev := sub.queue[0]
			sub.queue = sub.queue[1:]
			sub.mu.Unlock()
			return ev, nil
		case sub.closed:
			err := sub.err
			sub.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return Event{}, err
		}
		sub.mu.Unlock()
		select {
		case <-sub.wake:
		case <-ctx.Done():
			return Event{}, ctx.Err()
		}
	}
}

// markClosed stops accepting events; queued ones can still be read.
func (sub *Subscription) markClosed() {
	sub.mu.Lock()
	sub.closed = true
	sub.signal()
	sub.mu.Unlock()
}

// Close detaches the subscription from the manager and its sessions.
func (sub *Subscription) Close() {
	sub.markClosed()
	sub.m.unsubscribe(sub)
}

// envelopeLocked wraps raw as an event of this session; the caller holds s.mu.
func (s *Session) envelopeLocked(name string, raw json.RawMessage) Event {
	env := &Envelope{SessionID: s.ID, Origin: s.origin, LCP: raw}
	if s.autopilotActive() {
		env.Autopilot = s.ap.Mode
	}
	return Event{Name: name, Envelope: env}
}

// broadcastLocked hands an event to every subscriber; the caller holds s.mu.
func (s *Session) broadcastLocked(name string, raw json.RawMessage) {
	if len(s.plug) == 0 {
		return
	}
	ev := s.envelopeLocked(name, raw)
	for sub := range s.plug {
		sub.push(ev)
	}
}

// synthLocked broadcasts an event that gg did not emit itself.
func (s *Session) synthLocked(name string, fields map[string]any) {
	fields["event"] = name
	raw, err := json.Marshal(fields)
	if err != nil {
		return
	}
	s.broadcastLocked(name, raw)
}

// attach registers sub and replays the session's current state to it.
func (s *Session) attach(sub *Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plug[sub] = struct{}{}
	if s.state.Terminal() {
		return
	}
	replay := func(name string, raw json.RawMessage) {
		if raw != nil {
			sub.push(s.envelopeLocked(name, raw))
		}
	}
	replay("started", s.startedRaw)
	for _, m := range s.markRaws {
		replay("mark", m)
	}
	replay("autopilot", s.apRaw)
	if s.agentSteer {
		sub.push(s.agentSteerEventLocked())
	}
	replay("heartbeat", s.beatRaw)
}

func (s *Session) detach(sub *Subscription) {
	s.mu.Lock()
	delete(s.plug, sub)
	s.mu.Unlock()
}

// Subscribe returns a stream of every session's events from now on, preceded
// by a replay of each running session's state.
func (m *Manager) Subscribe() *Subscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub := newSubscription(m)
	if m.closed {
		sub.markClosed()
		return sub
	}
	m.purgeLocked()
	m.subs[sub] = struct{}{}
	for _, s := range m.sessions {
		s.attach(sub)
	}
	return sub
}

func (m *Manager) unsubscribe(sub *Subscription) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.subs, sub)
	for _, s := range m.sessions {
		s.detach(sub)
	}
}

// closeSubscriptions ends every subscription once its queue is read.
func (m *Manager) closeSubscriptions() {
	m.mu.Lock()
	subs := make([]*Subscription, 0, len(m.subs))
	for sub := range m.subs {
		subs = append(subs, sub)
	}
	m.mu.Unlock()
	for _, sub := range subs {
		sub.markClosed()
	}
}
