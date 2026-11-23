package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Edge 19 (sessions): the cap counts running simulations, atomically.
func TestManager_SessionCap(t *testing.T) {
	m := testManager(2)
	a := start(t, m, "normal")
	b := start(t, m, "normal")

	_, err := m.Start(context.Background(), fakeSpec(t, "normal"))
	var le *LimitError
	if !errors.As(err, &le) || le.Max != 2 || len(le.Running) != 2 || !strings.Contains(err.Error(), "--max-sessions") {
		t.Fatalf("err = %v", err)
	}

	a.Stop(context.Background(), StopOptions{Graceful: 3 * time.Second, Term: time.Second})
	c, err := m.Start(context.Background(), fakeSpec(t, "normal"))
	if err != nil {
		t.Fatalf("a finished session must free its slot: %v", err)
	}
	c.Stop(context.Background(), StopOptions{Graceful: 3 * time.Second, Term: time.Second})
	_ = b
}

func TestManager_ConcurrentStartsNeverExceedCap(t *testing.T) {
	m := testManager(3)
	var ok, limited atomic.Int32
	var mu sync.Mutex
	var started []*Session
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := m.Start(context.Background(), fakeSpec(t, "normal"))
			var le *LimitError
			switch {
			case err == nil:
				ok.Add(1)
				mu.Lock()
				started = append(started, s)
				mu.Unlock()
			case errors.As(err, &le):
				limited.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 3 || limited.Load() != 9 {
		t.Fatalf("started %d, limited %d; want 3 and 9", ok.Load(), limited.Load())
	}
	m.Shutdown(context.Background())
}

// Edge 21: unknown ids give a structured error; finished sessions are kept, then forgotten.
func TestManager_UnknownAndExpiredSessions(t *testing.T) {
	m := NewManager(ManagerOptions{MaxSessions: 2, Retention: 300 * time.Millisecond, StartTimeout: 5 * time.Second,
		Stop: StopOptions{Graceful: time.Second, Term: time.Second}})
	s := start(t, m, "finish-self")
	eventually(t, "finished", func() bool { return s.State() == Finished })

	got, err := m.Get(s.ID)
	if err != nil || got != s {
		t.Fatalf("a finished session stays queryable: %v", err)
	}

	_, err = m.Get("sim_nope")
	var nf *NotFoundError
	if !errors.As(err, &nf) || !strings.Contains(err.Error(), s.ID) || !strings.Contains(err.Error(), "kept for") {
		t.Fatalf("err = %v", err)
	}

	time.Sleep(450 * time.Millisecond)
	if _, err := m.Get(s.ID); !errors.As(err, &nf) {
		t.Fatalf("session should have expired, got %v", err)
	}
	if len(m.IDs()) != 0 {
		t.Errorf("ids = %v", m.IDs())
	}
}

func TestManager_ResolveEmptyID(t *testing.T) {
	m := testManager(3)
	if _, err := m.Resolve(""); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("no sessions: err = %v", err)
	}

	a := start(t, m, "normal")
	if got, err := m.Resolve(""); err != nil || got != a {
		t.Fatalf("one running session is the default: %v %v", got, err)
	}

	b := start(t, m, "normal")
	if _, err := m.Resolve(""); err == nil || !strings.Contains(err.Error(), a.ID) || !strings.Contains(err.Error(), b.ID) {
		t.Fatalf("ambiguous: err = %v", err)
	}

	a.Stop(context.Background(), StopOptions{Graceful: 3 * time.Second, Term: time.Second})
	if got, err := m.Resolve(""); err != nil || got != b {
		t.Fatalf("the only running one wins over a finished one: %v %v", got, err)
	}
}

// 10 concurrent start/stop cycles; run with -race.
func TestManager_ConcurrentStartStopCycles(t *testing.T) {
	m := testManager(10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := m.Start(context.Background(), fakeSpec(t, "normal"))
			if err != nil {
				t.Errorf("start: %v", err)
				return
			}
			_ = s.Telemetry()
			s.Stop(context.Background(), StopOptions{Graceful: 3 * time.Second, Term: time.Second})
			if s.State() != Stopped {
				t.Errorf("state = %s", s.State())
			}
			select {
			case <-s.Done():
			default:
				t.Error("process not reaped")
			}
		}()
	}
	wg.Wait()
	for _, id := range m.IDs() {
		s, _ := m.Get(id)
		if !s.State().Terminal() {
			t.Errorf("%s still %s", id, s.State())
		}
	}
}

func TestNotFoundError_Messages(t *testing.T) {
	e := &NotFoundError{ID: "x", Retention: 10 * time.Minute}
	if !strings.Contains(e.Error(), "no sessions") || !strings.Contains(e.Error(), "10m0s") {
		t.Errorf("%v", e)
	}
}
