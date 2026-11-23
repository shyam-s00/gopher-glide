package session

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func steerManager(reply, settle time.Duration) *Manager {
	return NewManager(ManagerOptions{
		MaxSessions: 4, Retention: time.Minute, StartTimeout: 5 * time.Second,
		Stop:         StopOptions{Graceful: time.Second, Term: 500 * time.Millisecond},
		ReplyTimeout: reply, SteerSettle: settle,
	})
}

func fixed(delta int) Planner {
	return func(Reading) (int, string, error) { return delta, "", nil }
}

func running(t *testing.T, m *Manager, scenario string) *Session {
	t.Helper()
	s := start(t, m, scenario)
	eventually(t, "running", func() bool { return s.State() == Running })
	return s
}

func TestSteerBias_ReplyCarriesCumulativeBiasAndIsRecorded(t *testing.T) {
	s := running(t, steerManager(2*time.Second, 50*time.Millisecond), "normal")

	a, err := s.SteerBias(context.Background(), "adjust_rps", fixed(5))
	if err != nil || a.BiasTotal != 5 || a.Delta != 5 || a.TargetBefore != 10 {
		t.Fatalf("first = %+v, %v", a, err)
	}
	b, err := s.SteerBias(context.Background(), "set_rps", fixed(-2))
	if err != nil || b.BiasTotal != 3 {
		t.Fatalf("second = %+v, %v", b, err)
	}

	steers := s.Telemetry().Steers
	if len(steers) != 2 || steers[0].Action != "adjust_rps" || steers[1].Action != "set_rps" || steers[1].BiasTotal != 3 || steers[1].AtS <= 0 {
		t.Fatalf("steers = %+v", steers)
	}
	eventually(t, "target to follow the bias", func() bool { return s.Telemetry().Metrics.TargetRPS == 13 })
}

func TestSteerBias_ZeroDeltaSendsNothing(t *testing.T) {
	s := running(t, steerManager(time.Second, 0), "normal")
	res, err := s.SteerBias(context.Background(), "set_rps", fixed(0))
	if err != nil || res.Delta != 0 || len(s.Telemetry().Steers) != 0 {
		t.Fatalf("res = %+v err = %v steers = %v", res, err, s.Telemetry().Steers)
	}
}

func TestSteerBias_PlannerErrorSendsNothing(t *testing.T) {
	s := running(t, steerManager(time.Second, 0), "normal")
	boom := errors.New("over the cap")
	_, err := s.SteerBias(context.Background(), "adjust_rps", func(Reading) (int, string, error) { return 5, "", boom })
	if !errors.Is(err, boom) || len(s.Telemetry().Steers) != 0 {
		t.Fatalf("err = %v steers = %v", err, s.Telemetry().Steers)
	}
}

// Edge 25: nothing is written once the run is over or going away.
func TestSteer_RefusedWhenNotRunning(t *testing.T) {
	s := start(t, steerManager(time.Second, 0), "finish-self")
	eventually(t, "finished", func() bool { return s.State().Terminal() })

	var nr *NotRunningError
	if _, err := s.SteerBias(context.Background(), "adjust_rps", fixed(5)); !errors.As(err, &nr) || !strings.Contains(err.Error(), "session_not_running") {
		t.Fatalf("bias on a finished session: %v", err)
	}
	if _, err := s.Mark(context.Background(), "x", nil); !errors.As(err, &nr) {
		t.Fatalf("mark on a finished session: %v", err)
	}
}

func TestSteer_RefusedWhileStopping(t *testing.T) {
	s := running(t, steerManager(time.Second, 0), "ignore-stop")
	go s.Stop(context.Background(), StopOptions{Graceful: 400 * time.Millisecond, Term: 400 * time.Millisecond})
	eventually(t, "stopping", func() bool { return s.State() == Stopping })

	var nr *NotRunningError
	if _, err := s.SteerBias(context.Background(), "adjust_rps", fixed(5)); !errors.As(err, &nr) || nr.State != Stopping {
		t.Fatalf("err = %v", err)
	}
}

func TestSteer_ChildDiesWhileWaiting(t *testing.T) {
	s := running(t, steerManager(5*time.Second, 0), "no-reply")
	go func() {
		time.Sleep(150 * time.Millisecond)
		s.kill()
	}()
	var nr *NotRunningError
	if _, err := s.Mark(context.Background(), "x", nil); !errors.As(err, &nr) {
		t.Fatalf("err = %v", err)
	}
}

func TestSteer_NoReplyTimesOutAndForgetsTheWaiter(t *testing.T) {
	s := running(t, steerManager(200*time.Millisecond, 0), "no-reply")
	begin := time.Now()
	_, err := s.SteerBias(context.Background(), "adjust_rps", fixed(5))
	if err == nil || !strings.Contains(err.Error(), "did not reply") || time.Since(begin) > 3*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(begin))
	}
	s.mu.Lock()
	left := len(s.pending)
	s.mu.Unlock()
	if left != 0 || len(s.Telemetry().Steers) != 0 {
		t.Errorf("pending = %d, steers = %v: an unconfirmed change must not be recorded", left, s.Telemetry().Steers)
	}
}

func TestSteer_ContextCancelStopsWaiting(t *testing.T) {
	s := running(t, steerManager(5*time.Second, 0), "no-reply")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := s.SteerBias(ctx, "adjust_rps", fixed(5)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestSteer_ChildRejectionSurfaces(t *testing.T) {
	s := running(t, steerManager(time.Second, 0), "reject-bias")
	var re *ReplyError
	if _, err := s.SteerBias(context.Background(), "adjust_rps", fixed(5)); !errors.As(err, &re) || re.Reason != "invalid_argument" {
		t.Fatalf("err = %v", err)
	}
	if len(s.Telemetry().Steers) != 0 {
		t.Error("a rejected change must not be recorded")
	}
}

// Edge 33: many callers at once, each gets its own reply.
func TestSteer_ConcurrentCallersGetTheirOwnReplies(t *testing.T) {
	s := running(t, steerManager(5*time.Second, 0), "normal")
	const n = 24

	var wg sync.WaitGroup
	totals := make([]int, n)
	offsets := make([]float64, n)
	errs := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			res, err := s.SteerBias(context.Background(), "adjust_rps", fixed(1))
			if err != nil {
				errs <- err
				return
			}
			totals[i] = res.BiasTotal
		}()
		go func() {
			defer wg.Done()
			at := float64(100 + i)
			res, err := s.Mark(context.Background(), "m", &at)
			if err != nil {
				errs <- err
				return
			}
			offsets[i] = res.ElapsedS
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	sort.Ints(totals)
	for i, v := range totals {
		if v != i+1 {
			t.Fatalf("each change must see its own cumulative total, got %v", totals)
		}
	}
	for i, got := range offsets {
		if got != float64(100+i) {
			t.Fatalf("mark %d got another caller's reply: %v", i, offsets)
		}
	}
	s.mu.Lock()
	left := len(s.pending)
	s.mu.Unlock()
	if left != 0 {
		t.Errorf("%d waiters leaked", left)
	}
}

func TestMark_SendsClientOffsetWhenSupported(t *testing.T) {
	s := running(t, steerManager(time.Second, 0), "normal")
	at := 7.25
	res, err := s.Mark(context.Background(), "cache-warm", &at)
	if err != nil || !res.ClientOffsetUsed || res.ElapsedS != 7.25 || res.Clamped || res.Warning != "" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	zero := 0.0
	if res, err = s.Mark(context.Background(), "start", &zero); err != nil || res.ElapsedS != 0 || !res.ClientOffsetUsed {
		t.Fatalf("zero offset: %+v %v", res, err)
	}
}

func TestMark_WithoutCapabilityFallsBackToServerTimeAndWarns(t *testing.T) {
	s := running(t, steerManager(time.Second, 0), "no-elapsed-cap")
	at := 7.25
	res, err := s.Mark(context.Background(), "x", &at)
	if err != nil || res.ClientOffsetUsed || res.Warning == "" || res.ElapsedS == 7.25 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if res, err = s.Mark(context.Background(), "y", nil); err != nil || res.Warning != "" {
		t.Fatalf("no offset asked, no warning expected: %+v %v", res, err)
	}
}

// Edge 28: a future offset is clamped by gg and reported.
func TestMark_FutureOffsetIsClampedAndReported(t *testing.T) {
	s := running(t, steerManager(time.Second, 0), "clamp")
	at := 99999.0
	res, err := s.Mark(context.Background(), "late", &at)
	if err != nil || !res.Clamped || res.ElapsedS >= 99999 || res.Warning == "" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestMark_RejectsBadInput(t *testing.T) {
	s := running(t, steerManager(time.Second, 0), "normal")
	neg := -1.0
	for name, f := range map[string]func() error{
		"empty label":    func() error { _, err := s.Mark(context.Background(), "", nil); return err },
		"long label":     func() error { _, err := s.Mark(context.Background(), strings.Repeat("x", 201), nil); return err },
		"negative start": func() error { _, err := s.Mark(context.Background(), "x", &neg); return err },
	} {
		if err := f(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if len(s.Telemetry().Marks) != 0 {
		t.Errorf("rejected marks must not reach gg: %v", s.Telemetry().Marks)
	}
}

func TestReading_SettlesOneWindowAfterABiasChange(t *testing.T) {
	s := running(t, steerManager(time.Second, 300*time.Millisecond), "normal")
	if r := s.reading(); !r.Available || !r.Settled || r.TargetRPS != 10 {
		t.Fatalf("before any change: %+v", r)
	}
	if _, err := s.SteerBias(context.Background(), "adjust_rps", fixed(4)); err != nil {
		t.Fatal(err)
	}
	if r := s.reading(); r.Settled || r.Bias != 4 {
		t.Fatalf("right after a change the heartbeat cannot be trusted yet: %+v", r)
	}
	eventually(t, "settled", func() bool {
		r := s.reading()
		return r.Settled && r.TargetRPS == 14 && r.BiasAtReading == 4
	})
}

func TestReading_NotAvailableBeforeFirstHeartbeat(t *testing.T) {
	s := start(t, steerManager(time.Second, 0), "no-heartbeat")
	if r := s.reading(); r.Available {
		t.Fatalf("reading = %+v", r)
	}
}
