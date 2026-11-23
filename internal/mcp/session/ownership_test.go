package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func ownershipManager() *Manager {
	return NewManager(ManagerOptions{
		MaxSessions: 1, MaxUserSessions: 1, Retention: time.Minute, StartTimeout: 5 * time.Second,
		Stop: StopOptions{Graceful: time.Second, Term: 500 * time.Millisecond},
	})
}

func TestManager_SlotsAreCountedPerOrigin(t *testing.T) {
	m := ownershipManager()
	startAs(t, m, "normal", OriginAgent)

	spec := fakeSpec(t, "normal")
	spec.Origin = OriginUser
	user, err := m.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("an agent run must not use up the user's slot: %v", err)
	}
	t.Cleanup(func() { user.Stop(context.Background(), StopOptions{Graceful: time.Second, Term: time.Second}) })

	var le *LimitError
	if _, err := m.Start(context.Background(), fakeSpec(t, "normal")); !errors.As(err, &le) ||
		le.Origin != OriginAgent || !strings.Contains(err.Error(), "--max-sessions") {
		t.Fatalf("second agent run: %v", err)
	}
	spec2 := fakeSpec(t, "normal")
	spec2.Origin = OriginUser
	if _, err := m.Start(context.Background(), spec2); !errors.As(err, &le) ||
		le.Origin != OriginUser || !strings.Contains(err.Error(), "--max-user-sessions") {
		t.Fatalf("second user run: %v", err)
	}
}

func TestManager_UserSlotDefaultsToOne(t *testing.T) {
	m := NewManager(ManagerOptions{MaxSessions: 5})
	if m.opts.MaxUserSessions != 1 {
		t.Fatalf("default = %d", m.opts.MaxUserSessions)
	}
}

func TestResolve_EmptyIDPrefersTheCallersOwnRun(t *testing.T) {
	m := ownershipManager()
	user := startAs(t, m, "normal", OriginUser)
	agent := startAs(t, m, "normal", OriginAgent)

	if got, err := m.Resolve(""); err != nil || got != agent {
		t.Fatalf("agent default with both live = %v, %v; want the agent's own run", got, err)
	}
	if got, err := m.ResolveFor("", OriginUser); err != nil || got != user {
		t.Fatalf("plugin default with both live = %v, %v; want the user's run", got, err)
	}

	agent.Stop(context.Background(), StopOptions{Graceful: 3 * time.Second, Term: time.Second})
	if got, err := m.Resolve(""); err != nil || got != user {
		t.Fatalf("with only a user run live the agent resolves to it: %v, %v", got, err)
	}
}

func TestResolve_AmbiguousErrorListsOriginAndState(t *testing.T) {
	m := NewManager(ManagerOptions{MaxSessions: 2, MaxUserSessions: 1, StartTimeout: 5 * time.Second,
		Stop: StopOptions{Graceful: time.Second, Term: time.Second}})
	startAs(t, m, "normal", OriginUser)
	a := startAs(t, m, "normal", OriginAgent)
	b := startAs(t, m, "normal", OriginAgent)

	_, err := m.Resolve("")
	if err == nil {
		t.Fatal("two agent runs are ambiguous even with a user run live")
	}
	for _, want := range []string{a.ID + " (agent", b.ID + " (agent", "(user"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestAgentAccess_UserRunIsLockedUntilTheUserOpensIt(t *testing.T) {
	m := ownershipManager()
	s := startAs(t, m, "normal", OriginUser)
	eventually(t, "a heartbeat", func() bool { return s.Telemetry().Metrics != nil })

	var locked *UserLockedError
	if _, err := s.Mark(context.Background(), "x", nil); !errors.As(err, &locked) || !strings.HasPrefix(err.Error(), "user_session_locked") {
		t.Fatalf("mark on a locked user run: %v", err)
	}
	if _, err := s.Hold(context.Background(), 5); !errors.As(err, &locked) {
		t.Errorf("hold: %v", err)
	}
	if err := s.Release(context.Background()); !errors.As(err, &locked) {
		t.Errorf("release: %v", err)
	}
	if _, err := s.SteerBias(context.Background(), "adjust_rps", func(Reading) (int, string, error) { return 5, "", nil }); !errors.As(err, &locked) {
		t.Errorf("bias: %v", err)
	}
	if err := s.StopAutopilot(context.Background()); !errors.As(err, &locked) {
		t.Errorf("autopilot stop: %v", err)
	}
	if tel := s.Telemetry(); tel.Origin != OriginUser || tel.AgentSteerAllowed == nil || *tel.AgentSteerAllowed {
		t.Errorf("telemetry must say the run is the user's and locked: %+v", tel)
	}

	s.SetAgentSteer(true)
	if _, err := s.Mark(context.Background(), "now allowed", nil); err != nil {
		t.Fatalf("mark after opening: %v", err)
	}
	s.SetAgentSteer(false)
	if _, err := s.Mark(context.Background(), "locked again", nil); !errors.As(err, &locked) {
		t.Fatalf("mark after closing: %v", err)
	}
}

func TestAgentAccess_AgentRunsAreAlwaysOpen(t *testing.T) {
	s := startAs(t, ownershipManager(), "normal", OriginAgent)
	if err := s.AgentAccess(); err != nil {
		t.Fatal(err)
	}
	if tel := s.Telemetry(); tel.AgentSteerAllowed != nil {
		t.Errorf("agent_steer_allowed only describes user runs: %+v", tel)
	}
}

func TestSetAgentSteer_TellsSubscribersAndReplays(t *testing.T) {
	m := ownershipManager()
	s := startAs(t, m, "normal", OriginUser)
	sub := m.Subscribe()
	defer sub.Close()

	s.SetAgentSteer(true)
	var ev Event
	for i := 0; i < 50; i++ {
		ev = next(t, sub)
		if ev.Name == "ack" && lcpField(t, ev, "command") == "agent_steer" {
			break
		}
	}
	if lcpField(t, ev, "allow") != true || ev.Envelope.Origin != OriginUser {
		t.Fatalf("event = %s", ev.JSON())
	}

	late := m.Subscribe()
	defer late.Close()
	var replayed bool
	for i := 0; i < 5 && !replayed; i++ {
		e := next(t, late)
		replayed = e.Name == "ack" && lcpField(t, e, "command") == "agent_steer" && lcpField(t, e, "allow") == true
	}
	if !replayed {
		t.Error("a panel opened late must learn the toggle state")
	}
}

func TestCommand_ForwardsAsUserAndKeepsTheCallersID(t *testing.T) {
	s := startAs(t, ownershipManager(), "normal", OriginUser)
	eventually(t, "a heartbeat", func() bool { return s.Telemetry().Metrics != nil })

	raw, err := s.Command(context.Background(), map[string]any{"id": "ui-7", "command": "bias", "amount": 5}, "user")
	if err != nil {
		t.Fatal(err)
	}
	var ack map[string]any
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("reply is not JSON: %v", err)
	}
	if ack["event"] != "ack" || ack["id"] != "ui-7" || ack["source"] != "user" || ack["bias"] != float64(5) {
		t.Errorf("ack = %v", ack)
	}

	raw, err = s.Command(context.Background(), map[string]any{"command": "mark", "label": "from the panel"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"id"`) {
		t.Errorf("a command sent without an id gets a reply without one: %s", raw)
	}
}

func TestCommand_ChildRefusalIsAReplyNotAnError(t *testing.T) {
	s := startAs(t, ownershipManager(), "reject-bias", OriginUser)
	eventually(t, "a heartbeat", func() bool { return s.Telemetry().Metrics != nil })

	raw, err := s.Command(context.Background(), map[string]any{"id": "x", "command": "bias", "amount": 5}, "user")
	if err != nil {
		t.Fatalf("a refusal travels as the reply: %v", err)
	}
	var ev map[string]any
	_ = json.Unmarshal(raw, &ev)
	if ev["event"] != "error" || ev["reason"] != "invalid_argument" || ev["id"] != "x" {
		t.Errorf("reply = %v", ev)
	}
}

func TestCommand_RejectsWhatThePluginMayNotSend(t *testing.T) {
	s := startAs(t, ownershipManager(), "normal", OriginUser)
	for _, c := range []string{"", "agent_steer", "shutdown", "BIAS"} {
		if _, err := s.Command(context.Background(), map[string]any{"command": c}, "user"); !errors.Is(err, ErrUnknownCommand) {
			t.Errorf("command %q: err = %v", c, err)
		}
	}
}

func TestCommand_StopEndsTheRunAndOverAFinishedRunIsRefused(t *testing.T) {
	m := ownershipManager()
	s := startAs(t, m, "normal", OriginUser)
	sub := m.Subscribe()
	defer sub.Close()

	raw, err := s.Command(context.Background(), map[string]any{"command": "stop"}, "user")
	if err != nil || !strings.Contains(string(raw), `"ack"`) {
		t.Fatalf("stop: %s, %v", raw, err)
	}
	nextNamed(t, sub, "stopped")
	<-s.Done()
	var nr *NotRunningError
	if _, err := s.Command(context.Background(), map[string]any{"command": "release"}, "user"); !errors.As(err, &nr) {
		t.Fatalf("after the run: %v", err)
	}
}

func TestCommand_WorksOnAnAgentRunToo(t *testing.T) {
	s := startAs(t, ownershipManager(), "normal", OriginAgent)
	eventually(t, "a heartbeat", func() bool { return s.Telemetry().Metrics != nil })
	if _, err := s.Command(context.Background(), map[string]any{"command": "hold", "rps": 9}, "user"); err != nil {
		t.Fatalf("the plugin may take over an agent's run: %v", err)
	}
	eventually(t, "the hold", func() bool { return s.Telemetry().HoldRPS == 9 })
}
