package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

func runningSession(t *testing.T, scenario string) *Session {
	t.Helper()
	s := start(t, testManager(2), scenario)
	eventually(t, "first heartbeat", func() bool { return s.State() == Running })
	return s
}

func TestWait_TimerEndsAfterTheWaitAndSummarisesReadings(t *testing.T) {
	s := runningSession(t, "normal")
	sum, err := s.Wait(context.Background(), Watch{Wait: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if sum.WakeReason != WakeTimer || sum.WaitedS < 0.29 || sum.WaitedS > 0.6 {
		t.Errorf("summary = %+v", sum)
	}
	if sum.Beats < 3 || sum.MinRPS != 9.88 || sum.MaxRPS != 9.88 || sum.MinP99Ms != 20 || sum.MaxP99Ms != 20 || sum.Basis != "window" {
		t.Errorf("readings = %+v", sum)
	}
}

func TestWait_WakesOnWindowedThresholds(t *testing.T) {
	cases := []struct {
		name   string
		watch  Watch
		reason string
	}{
		{"p99", Watch{Wait: 10 * time.Second, WakeP99Ms: 250}, WakeP99},
		{"error rate", Watch{Wait: 10 * time.Second, WakeErrRate: 0.15}, WakeErrorRate},
		{"any error", Watch{Wait: 10 * time.Second, WakeAnyError: true}, WakeErrorRate},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := runningSession(t, "degrade")
			sum, err := s.Wait(context.Background(), tt.watch)
			if err != nil {
				t.Fatal(err)
			}
			if sum.WakeReason != tt.reason || sum.WaitedS > 5 || sum.Beats == 0 {
				t.Errorf("summary = %+v", sum)
			}
			if sum.MaxP99Ms != 300 {
				t.Errorf("the breaching reading should be in the range: %+v", sum)
			}
		})
	}
}

// Cumulative error rate in these beats is 0.1; only the window is judged.
func TestWait_ThresholdsUseTheWindowNotTheCumulativeFigures(t *testing.T) {
	s := runningSession(t, "normal")
	sum, err := s.Wait(context.Background(), Watch{Wait: 400 * time.Millisecond, WakeErrRate: 0.05, WakeP99Ms: 30})
	if err != nil {
		t.Fatal(err)
	}
	// cumulative p99 is 55.5 and error rate 0.1, both over the thresholds; the window (20 ms, 0) is not.
	if sum.WakeReason != WakeTimer {
		t.Errorf("woke on cumulative figures: %+v", sum)
	}
}

func TestWait_FallsBackToCumulativeWhenNoWindow(t *testing.T) {
	s := runningSession(t, "no-window")
	sum, err := s.Wait(context.Background(), Watch{Wait: 5 * time.Second, WakeP99Ms: 50})
	if err != nil {
		t.Fatal(err)
	}
	if sum.WakeReason != WakeP99 || sum.Basis != "cumulative" || sum.MaxP99Ms != 55.5 {
		t.Errorf("summary = %+v", sum)
	}
}

func TestWait_RunEnded(t *testing.T) {
	t.Run("stopped during the wait", func(t *testing.T) {
		s := runningSession(t, "normal")
		go func() {
			time.Sleep(150 * time.Millisecond)
			s.Stop(context.Background(), StopOptions{Graceful: time.Second, Term: time.Second})
		}()
		begin := time.Now()
		sum, err := s.Wait(context.Background(), Watch{Wait: 20 * time.Second})
		if err != nil || sum.WakeReason != WakeRunEnded || time.Since(begin) > 5*time.Second {
			t.Errorf("summary = %+v err = %v after %v", sum, err, time.Since(begin))
		}
	})
	t.Run("already over", func(t *testing.T) {
		s := runningSession(t, "normal")
		s.Stop(context.Background(), StopOptions{Graceful: time.Second, Term: time.Second})
		sum, err := s.Wait(context.Background(), Watch{Wait: 20 * time.Second})
		if err != nil || sum.WakeReason != WakeRunEnded || sum.WaitedS > 0.5 {
			t.Errorf("summary = %+v err = %v", sum, err)
		}
	})
	t.Run("child dies", func(t *testing.T) {
		s := start(t, testManager(2), "die-after-start")
		sum, err := s.Wait(context.Background(), Watch{Wait: 20 * time.Second})
		if err != nil || sum.WakeReason != WakeRunEnded {
			t.Errorf("summary = %+v err = %v", sum, err)
		}
	})
}

func TestWait_CancelledByTheCaller(t *testing.T) {
	s := runningSession(t, "normal")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	begin := time.Now()
	_, err := s.Wait(ctx, Watch{Wait: 20 * time.Second})
	if !errors.Is(err, context.Canceled) || time.Since(begin) > 3*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(begin))
	}
}

func TestWait_RejectsNonPositive(t *testing.T) {
	s := runningSession(t, "normal")
	if _, err := s.Wait(context.Background(), Watch{}); err == nil {
		t.Error("a zero wait must be refused")
	}
}

func TestWait_ReleasesItsSubscription(t *testing.T) {
	s := runningSession(t, "normal")
	for i := 0; i < 3; i++ {
		_, _ = s.Wait(context.Background(), Watch{Wait: 60 * time.Millisecond})
	}
	s.mu.Lock()
	n := len(s.subs)
	s.mu.Unlock()
	if n != 0 {
		t.Errorf("%d subscriptions left behind", n)
	}
}

func TestWait_ConcurrentWaitersAreIndependent(t *testing.T) {
	s := runningSession(t, "degrade")
	type res struct {
		sum WindowSummary
		err error
	}
	out := make(chan res, 2)
	go func() {
		sum, err := s.Wait(context.Background(), Watch{Wait: 10 * time.Second, WakeP99Ms: 250})
		out <- res{sum, err}
	}()
	go func() {
		sum, err := s.Wait(context.Background(), Watch{Wait: 200 * time.Millisecond})
		out <- res{sum, err}
	}()
	reasons := map[string]bool{}
	for i := 0; i < 2; i++ {
		r := <-out
		if r.err != nil {
			t.Fatal(r.err)
		}
		reasons[r.sum.WakeReason] = true
	}
	if !reasons[WakeP99] && !reasons[WakeTimer] {
		t.Errorf("reasons = %v", reasons)
	}
}

func TestTelemetry_CarriesWindowAndHold(t *testing.T) {
	s := runningSession(t, "normal")
	tel := s.Telemetry()
	if tel.Window == nil || tel.Window.P99Ms != 20 || tel.Window.Samples != 100 || tel.HoldRPS != 0 {
		t.Fatalf("telemetry = %+v", tel)
	}
	if _, err := s.Hold(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	eventually(t, "held rate in telemetry", func() bool {
		tel := s.Telemetry()
		return tel.HoldRPS == 7 && tel.Metrics != nil && tel.Metrics.TargetRPS == 7
	})
}

func TestHold_ReleaseAndSteerLog(t *testing.T) {
	s := runningSession(t, "normal")
	res, err := s.Hold(context.Background(), 12)
	if err != nil || res.HoldRPS != 12 {
		t.Fatalf("hold = %+v %v", res, err)
	}
	if r := s.reading(); r.HoldRPS != 12 {
		t.Errorf("reading hold = %d, want it known straight after the ack", r.HoldRPS)
	}
	if err := s.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := s.reading(); r.HoldRPS != 0 {
		t.Errorf("reading hold after release = %d", r.HoldRPS)
	}
	steers := s.Telemetry().Steers
	if len(steers) != 2 || steers[0].Action != "hold_rps" || steers[0].HoldRPS != 12 || steers[1].Action != "release_hold" {
		t.Errorf("steers = %+v", steers)
	}
}

func TestHold_Refusals(t *testing.T) {
	t.Run("gg cannot hold", func(t *testing.T) {
		s := runningSession(t, "no-hold-cap")
		if _, err := s.Hold(context.Background(), 5); !errors.Is(err, ErrNoHold) {
			t.Errorf("hold err = %v", err)
		}
		if err := s.Release(context.Background()); !errors.Is(err, ErrNoHold) {
			t.Errorf("release err = %v", err)
		}
	})
	t.Run("gg rejects", func(t *testing.T) {
		s := runningSession(t, "reject-hold")
		var re *ReplyError
		if _, err := s.Hold(context.Background(), 5); !errors.As(err, &re) || re.Reason != "invalid_argument" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("not running", func(t *testing.T) {
		s := runningSession(t, "normal")
		s.Stop(context.Background(), StopOptions{Graceful: time.Second, Term: time.Second})
		var nr *NotRunningError
		if _, err := s.Hold(context.Background(), 5); !errors.As(err, &nr) {
			t.Errorf("err = %v", err)
		}
	})
}

// Every command the server sends on an agent's behalf names its source.
func TestSteering_CommandsCarrySourceAgent(t *testing.T) {
	s := runningSession(t, "require-agent-source")
	ctx := context.Background()
	if _, err := s.Hold(ctx, 8); err != nil {
		t.Errorf("hold: %v", err)
	}
	if err := s.Release(ctx); err != nil {
		t.Errorf("release: %v", err)
	}
	if _, err := s.Mark(ctx, "x", nil); err != nil {
		t.Errorf("mark: %v", err)
	}
	if _, err := s.SteerBias(ctx, "adjust_rps", func(Reading) (int, string, error) { return 2, "", nil }); err != nil {
		t.Errorf("bias: %v", err)
	}
}
