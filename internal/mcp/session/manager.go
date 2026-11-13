package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Defaults for ManagerOptions fields left at zero.
const (
	DefaultRetention    = 10 * time.Minute
	DefaultStartTimeout = 15 * time.Second
	DefaultGraceful     = 30 * time.Second
	DefaultTermGrace    = 5 * time.Second
)

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	// MaxSessions bounds agent simulations that are not yet over.
	MaxSessions int
	// MaxUserSessions bounds the same for user simulations; zero means one.
	MaxUserSessions int
	// Retention is how long a finished session stays queryable.
	Retention    time.Duration
	StartTimeout time.Duration
	Stop         StopOptions
	// ReplyTimeout is how long a steering command waits for gg's reply.
	ReplyTimeout time.Duration
	// SteerSettle is how long after a bias change a heartbeat is trusted to reflect it.
	SteerSettle time.Duration
}

// NotFoundError is returned for a session id that is unknown or expired.
type NotFoundError struct {
	ID     string
	Active []string
	// Retention is how long finished sessions stay available.
	Retention time.Duration
}

func (e *NotFoundError) Error() string {
	msg := fmt.Sprintf("unknown session_id %q", e.ID)
	if e.ID == "" {
		msg = "session_id is required"
	}
	if len(e.Active) > 0 {
		msg += "; known sessions: " + strings.Join(e.Active, ", ")
	} else {
		msg += "; there are no sessions"
	}
	return msg + fmt.Sprintf(" (finished sessions are kept for %s)", e.Retention)
}

// LimitError is returned when too many simulations are already running.
type LimitError struct {
	Origin  Origin
	Max     int
	Running []string
}

func (e *LimitError) Error() string {
	flag, who := "--max-sessions", "agent "
	if e.Origin == OriginUser {
		flag, who = "--max-user-sessions", "user "
	}
	return fmt.Sprintf("%d %ssimulation(s) already running (limit %d: %s); stop one first: %s",
		len(e.Running), who, e.Max, flag, strings.Join(e.Running, ", "))
}

// Manager owns the sessions of one server.
type Manager struct {
	opts ManagerOptions

	mu       sync.Mutex
	sessions map[string]*Session
	subs     map[*Subscription]struct{}
	closed   bool
}

// NewManager returns a Manager with defaults filled in.
func NewManager(o ManagerOptions) *Manager {
	if o.Retention <= 0 {
		o.Retention = DefaultRetention
	}
	if o.StartTimeout <= 0 {
		o.StartTimeout = DefaultStartTimeout
	}
	if o.ReplyTimeout <= 0 {
		o.ReplyTimeout = DefaultReplyTimeout
	}
	if o.SteerSettle <= 0 {
		o.SteerSettle = DefaultSteerSettle
	}
	if o.Stop.Graceful <= 0 {
		o.Stop.Graceful = DefaultGraceful
	}
	if o.Stop.Term <= 0 {
		o.Stop.Term = DefaultTermGrace
	}
	if o.MaxUserSessions <= 0 {
		o.MaxUserSessions = 1
	}
	return &Manager{opts: o, sessions: map[string]*Session{}, subs: map[*Subscription]struct{}{}}
}

// Start launches spec and returns once the child reports "started". The
// session limit is checked and a slot taken atomically, before any spawn.
func (m *Manager) Start(ctx context.Context, spec Spec) (*Session, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, fmt.Errorf("server is shutting down")
	}
	m.purgeLocked()
	origin, limit := OriginAgent, m.opts.MaxSessions
	if spec.Origin == OriginUser {
		origin, limit = OriginUser, m.opts.MaxUserSessions
	}
	if running := m.runningLocked(origin); limit > 0 && len(running) >= limit {
		m.mu.Unlock()
		return nil, &LimitError{Origin: origin, Max: limit, Running: running}
	}
	s := newSession(newID(), spec)
	s.replyTimeout, s.settle = m.opts.ReplyTimeout, m.opts.SteerSettle
	m.sessions[s.ID] = s
	for sub := range m.subs {
		s.attach(sub)
	}
	m.mu.Unlock()

	if err := s.launch(); err != nil {
		m.remove(s.ID)
		cleanup(spec)
		return nil, err
	}
	if err := s.waitStarted(ctx, m.opts.StartTimeout); err != nil {
		s.Stop(context.Background(), StopOptions{Graceful: time.Second, Term: time.Second})
		m.remove(s.ID)
		return nil, err
	}
	return s, nil
}

// Get returns the session with the given id.
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked()
	if s, ok := m.sessions[id]; ok && id != "" {
		return s, nil
	}
	return nil, m.notFoundLocked(id)
}

// Resolve is Get, except that an empty id selects the agent's own run: the
// only running agent session, else the only running user session, else the
// only one kept.
func (m *Manager) Resolve(id string) (*Session, error) { return m.ResolveFor(id, OriginAgent) }

// ResolveFor is Resolve with the caller's own origin tried first, so a plugin
// with an empty id gets its run and an agent gets its own.
func (m *Manager) ResolveFor(id string, prefer Origin) (*Session, error) {
	if id != "" {
		return m.Get(id)
	}
	other := OriginUser
	if prefer == OriginUser {
		other = OriginAgent
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked()
	mine, theirs := m.runningLocked(prefer), m.runningLocked(other)
	switch {
	case len(mine) == 1:
		return m.sessions[mine[0]], nil
	case len(mine) == 0 && len(theirs) == 1:
		return m.sessions[theirs[0]], nil
	case len(mine)+len(theirs) == 0 && len(m.sessions) == 1:
		for _, s := range m.sessions {
			return s, nil
		}
	}
	return nil, m.notFoundLocked("")
}

// IDs lists known session ids, oldest first.
func (m *Manager) IDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked()
	return m.idsLocked()
}

// Shutdown refuses new sessions and stops every running one.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	m.closed = true
	var live []*Session
	for _, s := range m.sessions {
		if !s.State().Terminal() {
			live = append(live, s)
		}
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range live {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			s.Stop(ctx, m.opts.Stop)
		}(s)
	}
	wg.Wait()
	m.closeSubscriptions()
}

// StopOptions returns the escalation timings sessions use.
func (m *Manager) StopOptions() StopOptions { return m.opts.Stop }

func (m *Manager) remove(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

func cleanup(spec Spec) {
	if spec.Dir != "" {
		_ = os.RemoveAll(spec.Dir)
	}
}

// runningLocked lists the ids of simulations not yet over; origin "" means any.
func (m *Manager) runningLocked(origin ...Origin) []string {
	var ids []string
	for id, s := range m.sessions {
		if s.State().Terminal() || (len(origin) > 0 && s.origin != origin[0]) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (m *Manager) idsLocked() []string {
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (m *Manager) notFoundLocked(id string) error {
	active := m.idsLocked()
	if id == "" {
		for i, sid := range active {
			s := m.sessions[sid]
			active[i] = fmt.Sprintf("%s (%s, %s)", sid, s.origin, s.State())
		}
	}
	return &NotFoundError{ID: id, Active: active, Retention: m.opts.Retention}
}

// purgeLocked forgets finished sessions older than the retention period.
func (m *Manager) purgeLocked() {
	now := time.Now()
	for id, s := range m.sessions {
		s.mu.Lock()
		expired := s.state.Terminal() && !s.endedAt.IsZero() && now.Sub(s.endedAt) > m.opts.Retention
		s.mu.Unlock()
		if expired {
			delete(m.sessions, id)
		}
	}
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "sim_" + hex.EncodeToString(b[:])
}
