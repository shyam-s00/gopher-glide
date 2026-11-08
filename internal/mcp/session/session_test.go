package session

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func testManager(max int) *Manager {
	return NewManager(ManagerOptions{
		MaxSessions: max, Retention: time.Minute, StartTimeout: 5 * time.Second,
		Stop: StopOptions{Graceful: time.Second, Term: 500 * time.Millisecond},
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func start(t *testing.T, m *Manager, scenario string) *Session {
	t.Helper()
	s, err := m.Start(context.Background(), fakeSpec(t, scenario))
	if err != nil {
		t.Fatalf("start %s: %v", scenario, err)
	}
	t.Cleanup(func() { s.Stop(context.Background(), StopOptions{Graceful: time.Second, Term: time.Second}) })
	return s
}

func TestStart_ParsesStartedAndGoesRunningOnFirstHeartbeat(t *testing.T) {
	s := start(t, testManager(2), "normal")
	stages, caps, proto, ok := s.Started()
	if !ok || len(stages) != 2 || stages[1].Name != "Sustain" || proto != 1 || len(caps) != 4 {
		t.Fatalf("started = %+v %v %d ok=%v", stages, caps, proto, ok)
	}
	eventually(t, "first heartbeat", func() bool { return s.State() == Running })

	tel := s.Telemetry()
	if tel.Metrics == nil || tel.Metrics.TargetRPS != 10 || tel.Metrics.ActualRPS != 9.88 || tel.Metrics.P50Ms != 12.35 {
		t.Fatalf("metrics = %+v", tel.Metrics)
	}
	if tel.Stage != "1/2 Ramp Up" {
		t.Errorf("stage = %q", tel.Stage)
	}
	if tel.ErrorMix == nil || tel.ErrorMix.HTTP5xx == 0 || tel.ErrorMix.TopCodes[0].Code != 503 {
		t.Errorf("error_mix must pass through: %+v", tel.ErrorMix)
	}
	if tel.ElapsedS <= 0 {
		t.Errorf("elapsed = %v", tel.ElapsedS)
	}
}

// Before the first heartbeat there are no numbers to report, not stale ones.
func TestTelemetry_StartingStateHasNoNumbers(t *testing.T) {
	s := start(t, testManager(2), "no-heartbeat")
	tel := s.Telemetry()
	if tel.State != Starting || tel.Metrics != nil || tel.Final != nil || tel.Stage != "" {
		t.Fatalf("telemetry = %+v", tel)
	}
	raw, _ := json.Marshal(tel)
	if strings.Contains(string(raw), "target_rps") {
		t.Errorf("no numbers expected: %s", raw)
	}
}

func TestStop_GracefulReturnsFinalStatsAndSnapshotPath(t *testing.T) {
	s := start(t, testManager(2), "normal")
	eventually(t, "running", func() bool { return s.State() == Running })

	s.Stop(context.Background(), StopOptions{Graceful: 3 * time.Second, Term: time.Second})
	tel := s.Telemetry()
	if tel.State != Stopped || tel.Final == nil || tel.Final.Total != 100 || tel.Final.Failure != 10 || tel.Metrics != nil {
		t.Fatalf("telemetry = %+v", tel)
	}
	if tel.SnapshotPath != "/tmp/gg snaps/run-1.snap" {
		t.Errorf("snapshot path = %q (paths with spaces and a trailing note must parse)", tel.SnapshotPath)
	}
	select {
	case <-s.Done():
	default:
		t.Error("Done must be closed after Stop returns")
	}
}

func TestStop_IsIdempotentAndSafeConcurrently(t *testing.T) {
	s := start(t, testManager(2), "normal")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Stop(context.Background(), StopOptions{Graceful: 3 * time.Second, Term: time.Second})
		}()
	}
	wg.Wait()
	s.Stop(context.Background(), StopOptions{Graceful: time.Second, Term: time.Second}) // after the end
	if s.State() != Stopped {
		t.Fatalf("state = %s", s.State())
	}
}

// A child that ignores stop is terminated; one that also ignores SIGTERM is killed.
func TestStop_EscalatesToTermThenKill(t *testing.T) {
	for _, scenario := range []string{"ignore-stop", "ignore-term"} {
		t.Run(scenario, func(t *testing.T) {
			s := start(t, testManager(2), scenario)
			begin := time.Now()
			s.Stop(context.Background(), StopOptions{Graceful: 300 * time.Millisecond, Term: 300 * time.Millisecond})
			if took := time.Since(begin); took > 3*time.Second {
				t.Fatalf("stop took %s", took)
			}
			select {
			case <-s.Done():
			default:
				t.Fatal("process still alive after Stop")
			}
			if s.State() != Failed {
				t.Errorf("a killed child never reported a result; state = %s", s.State())
			}
		})
	}
}

// Edge: the child dies before "started".
func TestStart_ChildFailsBeforeStarted_ReportsStderr(t *testing.T) {
	m := testManager(2)
	_, err := m.Start(context.Background(), fakeSpec(t, "no-started"))
	if err == nil || !strings.Contains(err.Error(), "no stages defined") {
		t.Fatalf("err = %v, want the child's own message", err)
	}
	if len(m.IDs()) != 0 {
		t.Errorf("a failed start must not leave a session behind: %v", m.IDs())
	}
	// And the slot is free again.
	m2 := testManager(1)
	_, _ = m2.Start(context.Background(), fakeSpec(t, "no-started"))
	s := start(t, m2, "normal")
	_ = s
}

func TestStart_BadExecutable(t *testing.T) {
	m := testManager(1)
	_, err := m.Start(context.Background(), Spec{Exe: "/definitely/not/here"})
	if err == nil || len(m.IDs()) != 0 {
		t.Fatalf("err=%v ids=%v", err, m.IDs())
	}
}

func TestStart_TimeoutKillsChild(t *testing.T) {
	m := NewManager(ManagerOptions{MaxSessions: 1, StartTimeout: 300 * time.Millisecond})
	begin := time.Now()
	_, err := m.Start(context.Background(), fakeSpec(t, "slow-start"))
	if err == nil || !strings.Contains(err.Error(), "did not start") || time.Since(begin) > 5*time.Second {
		t.Fatalf("err=%v after %s", err, time.Since(begin))
	}
	if len(m.IDs()) != 0 {
		t.Error("session left behind")
	}
}

func TestStart_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	m := testManager(1)
	if _, err := m.Start(ctx, fakeSpec(t, "slow-start")); err == nil {
		t.Fatal("expected cancellation error")
	}
}

// Edge 22: the child dies mid-run; telemetry still answers, with the stderr tail.
func TestChildCrash_StateFailedWithStderrTail(t *testing.T) {
	s := start(t, testManager(2), "die-after-start")
	eventually(t, "failed", func() bool { return s.State() == Failed })

	tel := s.Telemetry()
	if tel.Message == "" || !strings.Contains(tel.StderrTail, "panic: boom") {
		t.Fatalf("telemetry = %+v", tel)
	}
	if len(tel.StderrTail) > maxStderrInTelemetry+8 {
		t.Errorf("stderr tail is %d bytes", len(tel.StderrTail))
	}
	if tel.Metrics != nil {
		t.Errorf("a dead run must not present live metrics: %+v", tel.Metrics)
	}
}

func TestChildFinishesOnItsOwn(t *testing.T) {
	s := start(t, testManager(2), "finish-self")
	eventually(t, "finished", func() bool { return s.State() == Finished })
	tel := s.Telemetry()
	if tel.Final == nil || tel.Final.Total != 50 || tel.SnapshotPath != "" || tel.Message != "Load test completed" {
		t.Fatalf("telemetry = %+v", tel)
	}
}

func TestWriteLine_RefusedOnceTerminal(t *testing.T) {
	s := start(t, testManager(2), "finish-self")
	eventually(t, "finished", func() bool { return s.State().Terminal() })
	err := s.WriteLine(`{"command":"bias","amount":5}`)
	if err == nil || !strings.Contains(err.Error(), "is finished") {
		t.Fatalf("writing to a finished session must be refused with its state, got %v", err)
	}
}

func TestWriteLine_ReachesChild(t *testing.T) {
	s := start(t, testManager(2), "normal")
	eventually(t, "running", func() bool { return s.State() == Running })
	if err := s.WriteLine(`{"id":"m1","command":"mark","label":"hello","elapsed_s":2.5}`); err != nil {
		t.Fatal(err)
	}
	eventually(t, "mark echo", func() bool { return len(s.Telemetry().Marks) == 1 })
	if m := s.Telemetry().Marks[0]; m.Label != "hello" || m.ElapsedS != 2.5 {
		t.Errorf("mark = %+v", m)
	}
}

func TestTelemetry_MarksBoundedAndLabelsClipped(t *testing.T) {
	s := start(t, testManager(2), "normal")
	eventually(t, "running", func() bool { return s.State() == Running })
	for i := 0; i < 12; i++ {
		_ = s.WriteLine(`{"id":"m","command":"mark","label":"` + strings.Repeat("x", 300) + `","elapsed_s":` + string(rune('0'+i%10)) + `}`)
	}
	eventually(t, "marks", func() bool { return len(s.Telemetry().Marks) == maxRecentMarks })
	time.Sleep(100 * time.Millisecond)
	tel := s.Telemetry()
	if len(tel.Marks) != maxRecentMarks || len(tel.Marks[0].Label) > 90 {
		t.Fatalf("marks = %d, first label %d bytes", len(tel.Marks), len(tel.Marks[0].Label))
	}
}

// The compact view must stay small enough to poll.
func TestTelemetry_SerializedSizeBounds(t *testing.T) {
	s := start(t, testManager(2), "normal")
	eventually(t, "running", func() bool { return s.State() == Running })
	raw, _ := json.Marshal(s.Telemetry())
	if len(raw) > 1500 {
		t.Errorf("typical telemetry is %d bytes (limit 1500): %s", len(raw), raw)
	}

	for i := 0; i < 8; i++ {
		_ = s.WriteLine(`{"id":"m","command":"mark","label":"` + strings.Repeat("é", 200) + `","elapsed_s":3}`)
	}
	for i := 0; i < 8; i++ {
		if _, err := s.SteerBias(context.Background(), "adjust_rps", fixed(-1234567)); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(150 * time.Millisecond)
	raw, _ = json.Marshal(s.Telemetry())
	if len(raw) > 4096 || len(s.Telemetry().Steers) != maxRecentSteers {
		t.Errorf("worst-case telemetry is %d bytes (limit 4096), steers kept %d", len(raw), len(s.Telemetry().Steers))
	}

	d := start(t, testManager(2), "die-after-start")
	eventually(t, "failed", func() bool { return d.State() == Failed })
	raw, _ = json.Marshal(d.Telemetry())
	if len(raw) > 4096 {
		t.Errorf("failed-session telemetry is %d bytes (limit 4096)", len(raw))
	}
}

func TestTelemetry_ElapsedFreezesAfterEnd(t *testing.T) {
	s := start(t, testManager(2), "finish-self")
	eventually(t, "finished", func() bool { return s.State() == Finished })
	a := s.Telemetry().ElapsedS
	time.Sleep(200 * time.Millisecond)
	if b := s.Telemetry().ElapsedS; a != b {
		t.Errorf("elapsed moved after the end: %v -> %v", a, b)
	}
}

// Closing the child's stdin (the parent going away) is what the real flag keys off.
func TestFakeEOFChild_ExitsWhenStdinCloses(t *testing.T) {
	s := start(t, testManager(2), "eof-exit")
	_ = s.stdin.Close()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit on stdin EOF")
	}
}

func TestSessionDirRemovedWhenProcessEnds(t *testing.T) {
	spec := fakeSpec(t, "finish-self")
	spec.Dir = t.TempDir() + "/run"
	if err := os.MkdirAll(spec.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := testManager(1).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	<-s.Done()
	if _, err := os.Stat(spec.Dir); !os.IsNotExist(err) {
		t.Errorf("input dir should be removed, stat err = %v", err)
	}
}

func TestStartupFailureAlsoRemovesDir(t *testing.T) {
	spec := fakeSpec(t, "no-started")
	spec.Dir = t.TempDir() + "/run"
	_ = os.MkdirAll(spec.Dir, 0o700)
	_, _ = testManager(1).Start(context.Background(), spec)
	if _, err := os.Stat(spec.Dir); !os.IsNotExist(err) {
		t.Errorf("dir should be gone, stat err = %v", err)
	}
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func TestShutdown_StopsEverythingAndLeaksNothing(t *testing.T) {
	m := testManager(4)
	var pids []int
	for _, sc := range []string{"normal", "ignore-stop", "ignore-term", "no-heartbeat"} {
		s, err := m.Start(context.Background(), fakeSpec(t, sc))
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, s.cmd.Process.Pid)
	}
	m.Shutdown(context.Background())
	for _, pid := range pids {
		eventually(t, "process gone", func() bool { return !processAlive(pid) })
	}
	if _, err := m.Start(context.Background(), fakeSpec(t, "normal")); err == nil {
		t.Error("no new sessions after shutdown")
	}
}

func TestExitReasonAndSnapshotPathHelpers(t *testing.T) {
	cases := map[string]string{
		"✓ Snapshot saved → /a/b.snap":                "/a/b.snap",
		"✓ Snapshot saved → /a b/c.snap  ⚠ 2 dropped": "/a b/c.snap",
		"Load test completed":                         "",
		"⚠  snap error: disk full":                    "",
		"":                                            "",
	}
	for in, want := range cases {
		if got := snapshotPath(in); got != want {
			t.Errorf("snapshotPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func plannedSpec(t *testing.T, scenario string, planned time.Duration) Spec {
	s := fakeSpec(t, scenario)
	s.Planned = planned
	return s
}

// A run stopped before its planned length says so, so a partial snapshot is not mistaken for a full one.
func TestTelemetry_StoppedEarlyIsCalledOut(t *testing.T) {
	m := testManager(2)
	s, err := m.Start(context.Background(), plannedSpec(t, "normal", 60*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "running", func() bool { return s.State() == Running })
	s.Stop(context.Background(), StopOptions{Graceful: 3 * time.Second, Term: time.Second})

	tel := s.Telemetry()
	if tel.State != Stopped || tel.PlannedS != 60 || !strings.Contains(tel.Message, "stopped early") || !strings.Contains(tel.Message, "of 60s planned") {
		t.Fatalf("telemetry = %+v", tel)
	}
	if tel.SnapshotPath == "" {
		t.Error("the snapshot path must still be reported")
	}
}

func TestTelemetry_StoppedNearPlannedLengthIsNotEarly(t *testing.T) {
	m := testManager(2)
	s, err := m.Start(context.Background(), plannedSpec(t, "normal", 1*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "running", func() bool { return s.State() == Running })
	s.Stop(context.Background(), StopOptions{Graceful: 3 * time.Second, Term: time.Second})
	if msg := s.Telemetry().Message; strings.Contains(msg, "early") {
		t.Errorf("within the slack of the plan, but got %q", msg)
	}
}

func TestTelemetry_NoPlannedLengthMeansNoEarlyClaim(t *testing.T) {
	s := start(t, testManager(2), "normal")
	eventually(t, "running", func() bool { return s.State() == Running })
	s.Stop(context.Background(), StopOptions{Graceful: 3 * time.Second, Term: time.Second})
	tel := s.Telemetry()
	if tel.PlannedS != 0 || strings.Contains(tel.Message, "early") {
		t.Errorf("telemetry = %+v", tel)
	}
}

func TestTelemetry_FinishedOnItsOwnIsNeverEarly(t *testing.T) {
	m := testManager(2)
	s, err := m.Start(context.Background(), plannedSpec(t, "finish-self", 60*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "finished", func() bool { return s.State() == Finished })
	if msg := s.Telemetry().Message; strings.Contains(msg, "early") {
		t.Errorf("a run that finished by itself was called early: %q", msg)
	}
}
