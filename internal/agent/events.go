package agent

import (
	"context"
	"sync"
	"time"

	"google.golang.org/adk/v2/session"
)

// EventKind says what an Event carries.
type EventKind string

// Event kinds.
const (
	KindThought    EventKind = "thought"
	KindText       EventKind = "text"
	KindToolCall   EventKind = "tool_call"
	KindToolResult EventKind = "tool_result"
	KindPlan       EventKind = "plan"
)

// Event is one step of an agent run, in a form any front end can render.
type Event struct {
	Kind   EventKind
	Text   string
	ToolID string
	Tool   string
	Args   map[string]any
	Result map[string]any
	// Plan is the full step list for KindPlan.
	Plan []PlanStep
}

// PlanStep is one line of a plan.
type PlanStep struct {
	Title  string
	Status string // pending, in_progress or completed
}

// Sink receives events as they happen. It must not block for long.
type Sink func(Event)

// runTracker remembers the simulations an agent started so they can be
// stopped if the agent is cancelled or ends without stopping them.
type runTracker struct {
	b *Backend

	mu   sync.Mutex
	live map[string]struct{}
}

func newRunTracker(b *Backend) *runTracker {
	return &runTracker{b: b, live: map[string]struct{}{}}
}

func (t *runTracker) observe(ev *session.Event) {
	if ev == nil || ev.Content == nil {
		return
	}
	for _, p := range ev.Content.Parts {
		fr := p.FunctionResponse
		if fr == nil {
			continue
		}
		id, _ := flatten(fr.Response)["session_id"].(string)
		if id == "" {
			continue
		}
		t.mu.Lock()
		switch fr.Name {
		case "start_simulation":
			t.live[id] = struct{}{}
		case "stop_simulation":
			delete(t.live, id)
		}
		t.mu.Unlock()
	}
}

// cleanup stops what is still running; it uses a fresh context because the run's may be cancelled.
func (t *runTracker) cleanup() {
	t.mu.Lock()
	ids := make([]string, 0, len(t.live))
	for id := range t.live {
		ids = append(ids, id)
	}
	t.live = map[string]struct{}{}
	t.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	StopRuns(t.b, ids)
}

// StopRuns stops simulations by id, ignoring ones that already ended.
func StopRuns(b *Backend, ids []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cs, err := b.Client(ctx)
	if err != nil {
		return
	}
	for _, id := range ids {
		_ = callTool(ctx, cs, "stop_simulation", map[string]any{"session_id": id}, nil)
	}
}

// flatten unwraps the {"output": ...} envelope ADK puts around MCP tool results.
func flatten(resp map[string]any) map[string]any {
	switch o := resp["output"].(type) {
	case map[string]any:
		return o
	case string:
		return map[string]any{"text": o}
	}
	return resp
}
