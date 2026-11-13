package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func startAs(t *testing.T, m *Manager, scenario string, origin Origin) *Session {
	t.Helper()
	spec := fakeSpec(t, scenario)
	spec.Origin = origin
	s, err := m.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("start %s (%s): %v", scenario, origin, err)
	}
	t.Cleanup(func() { s.Stop(context.Background(), StopOptions{Graceful: time.Second, Term: time.Second}) })
	return s
}

func next(t *testing.T, sub *Subscription) Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ev, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	return ev
}

// nextNamed skips events until one with the name arrives.
func nextNamed(t *testing.T, sub *Subscription, name string) Event {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ev := next(t, sub); ev.Name == name {
			return ev
		}
	}
	t.Fatalf("no %q event", name)
	return Event{}
}

func lcpField(t *testing.T, ev Event, key string) any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(ev.Envelope.LCP, &m); err != nil {
		t.Fatalf("lcp is not JSON: %v", err)
	}
	return m[key]
}

func TestSubscribe_ReplaysStateThenStreamsLive(t *testing.T) {
	m := testManager(2)
	s := startAs(t, m, "normal", OriginAgent)
	eventually(t, "a heartbeat", func() bool { return s.Telemetry().Metrics != nil })
	if _, err := s.Mark(context.Background(), "before subscribe", nil); err != nil {
		t.Fatal(err)
	}

	sub := m.Subscribe()
	defer sub.Close()

	var names []string
	for _, want := range []string{"started", "mark", "heartbeat"} {
		ev := next(t, sub)
		names = append(names, ev.Name)
		if ev.Name != want {
			t.Fatalf("replay order = %v, want started, mark, heartbeat", names)
		}
		if ev.Envelope.SessionID != s.ID || ev.Envelope.Origin != OriginAgent {
			t.Fatalf("envelope = %+v", ev.Envelope)
		}
	}

	if _, err := s.Mark(context.Background(), "after subscribe", nil); err != nil {
		t.Fatal(err)
	}
	ev := nextNamed(t, sub, "mark")
	if got := lcpField(t, ev, "label"); got != "after subscribe" {
		t.Errorf("live mark label = %v", got)
	}
	if got := lcpField(t, ev, "source"); got != "agent" {
		t.Errorf("source = %v; the agent's steers are tagged", got)
	}
}

func TestSubscribe_PayloadIsGgsOwnBytes(t *testing.T) {
	m := testManager(2)
	sub := m.Subscribe()
	defer sub.Close()
	startAs(t, m, "normal", OriginUser)

	ev := next(t, sub)
	if ev.Name != "started" || ev.Envelope.Origin != OriginUser {
		t.Fatalf("first event = %+v", ev)
	}
	// The fake prints compact JSON with sorted keys; a re-marshal of the parsed
	// payload would add every zero-valued field.
	if got := string(ev.Envelope.LCP); !strings.HasPrefix(got, `{"capabilities":[`) || strings.Contains(got, `"stage":`) {
		t.Errorf("lcp should be the child's line verbatim: %s", got)
	}
	if !json.Valid(ev.JSON()) {
		t.Errorf("event JSON invalid: %s", ev.JSON())
	}
}

func TestSubscribe_TwoSubscribersAndASessionStartedLater(t *testing.T) {
	m := testManager(2)
	a, b := m.Subscribe(), m.Subscribe()
	defer a.Close()
	defer b.Close()
	s := startAs(t, m, "normal", OriginAgent)

	for _, sub := range []*Subscription{a, b} {
		if ev := nextNamed(t, sub, "started"); ev.Envelope.SessionID != s.ID {
			t.Errorf("subscriber saw %s, want %s", ev.Envelope.SessionID, s.ID)
		}
	}
}

func TestSubscribe_ClosedSubscriberStopsReceiving(t *testing.T) {
	m := testManager(2)
	sub := m.Subscribe()
	sub.Close()
	startAs(t, m, "normal", OriginAgent)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := sub.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
	m.mu.Lock()
	n := len(m.subs)
	m.mu.Unlock()
	if n != 0 {
		t.Errorf("manager still tracks %d subscriptions", n)
	}
}

func TestSubscribe_EnvelopeNamesTheAutopilot(t *testing.T) {
	m := testManager(2)
	sub := m.Subscribe()
	defer sub.Close()
	startAs(t, m, "autopilot-ceiling", OriginUser)

	ev := nextNamed(t, sub, "heartbeat")
	if ev.Envelope.Autopilot != "find-ceiling" {
		t.Errorf("autopilot = %q", ev.Envelope.Autopilot)
	}
	if got := nextNamed(t, sub, "autopilot"); lcpField(t, got, "outcome") != "ceiling_found" {
		t.Errorf("autopilot event = %s", got.Envelope.LCP)
	}
	fin := nextNamed(t, sub, "finished")
	if fin.Envelope.Autopilot != "find-ceiling" {
		// the controller is done by then; the mode may or may not still be named
		t.Logf("finished envelope autopilot = %q", fin.Envelope.Autopilot)
	}
}

func TestSubscribe_ChildDyingWithoutAResultIsAnnounced(t *testing.T) {
	m := testManager(2)
	sub := m.Subscribe()
	defer sub.Close()
	startAs(t, m, "die-after-start", OriginUser)

	ev := nextNamed(t, sub, "failed")
	if msg, _ := lcpField(t, ev, "message").(string); !strings.Contains(msg, "without a result") {
		t.Errorf("failed message = %q", msg)
	}
}

func TestShutdown_DeliversTerminalEventsThenEndsStreams(t *testing.T) {
	m := testManager(2)
	startAs(t, m, "normal", OriginAgent)
	sub := m.Subscribe()
	defer sub.Close()

	m.Shutdown(context.Background())
	nextNamed(t, sub, "stopped")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, err := sub.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("err = %v, want EOF after the queue drains", err)
		}
	}
	if late := m.Subscribe(); late != nil {
		if _, err := late.Next(ctx); !errors.Is(err, io.EOF) {
			t.Errorf("a subscriber after shutdown must end at once, got %v", err)
		}
	}
}

func hb() Event           { return Event{Name: "heartbeat", Envelope: &Envelope{}} }
func crit(n string) Event { return Event{Name: n, Envelope: &Envelope{}} }

// A slow reader loses the oldest heartbeats, never the other events, and is told.
func TestSubscription_SlowReaderLosesOnlyHeartbeats(t *testing.T) {
	sub := newSubscription(nil)
	sub.push(crit("started"))
	for i := 0; i < subBuffer+100; i++ {
		sub.push(hb())
		if i%50 == 0 {
			sub.push(crit("mark"))
		}
	}
	sub.push(crit("finished"))

	ctx := context.Background()
	first, _ := sub.Next(ctx)
	if first.Name != "lagged" || first.Dropped == 0 {
		t.Fatalf("first = %+v, want a lagged notice", first)
	}
	var marks, beats int
	var names []string
	for len(sub.queue) > 0 {
		ev, _ := sub.Next(ctx)
		names = append(names, ev.Name)
		switch ev.Name {
		case "mark":
			marks++
		case "heartbeat":
			beats++
		}
	}
	if names[0] != "started" || names[len(names)-1] != "finished" {
		t.Errorf("unlosable events must keep their order: first %s last %s", names[0], names[len(names)-1])
	}
	if marks != (subBuffer+100+49)/50 {
		t.Errorf("marks kept = %d, want all", marks)
	}
	if want := subBuffer + 100 - first.Dropped; beats != want {
		t.Errorf("heartbeats kept = %d, dropped %d, want %d kept", beats, first.Dropped, want)
	}
}

func TestSubscription_QueueOfUnlosableEventsIsCutOff(t *testing.T) {
	sub := newSubscription(nil)
	for i := 0; i < subHardCap+10; i++ {
		sub.push(crit("ack"))
	}
	var err error
	for err == nil {
		_, err = sub.Next(context.Background())
	}
	if !errors.Is(err, ErrSlowSubscriber) {
		t.Fatalf("err = %v, want ErrSlowSubscriber", err)
	}
}

func TestSubscription_NextHonoursContext(t *testing.T) {
	sub := newSubscription(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := sub.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}
