package session

import (
	"context"
	"errors"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/ui"
)

// MaxWait is the longest a single wait may block.
const MaxWait = 30 * time.Second

// Wake reasons.
const (
	WakeTimer     = "timer"
	WakeP99       = "p99_threshold"
	WakeErrorRate = "error_threshold"
	WakeRunEnded  = "run_ended"
)

// beatBuffer is how many heartbeats a waiter can fall behind before it drops one.
const beatBuffer = 64

// Watch says how long to wait for a reading and what ends the wait early.
// Thresholds apply to the heartbeat's windowed figures and are skipped when zero.
type Watch struct {
	Wait         time.Duration
	WakeP99Ms    float64
	WakeErrRate  float64
	WakeAnyError bool
}

// WindowSummary reports what a wait saw.
type WindowSummary struct {
	WaitedS    float64 `json:"waited_s"`
	WakeReason string  `json:"wake_reason"`
	// Beats is how many readings arrived during the wait; the ranges are empty without any.
	Beats    int     `json:"beats"`
	MinRPS   float64 `json:"min_actual_rps,omitempty"`
	MaxRPS   float64 `json:"max_actual_rps,omitempty"`
	MinP99Ms float64 `json:"min_p99_ms,omitempty"`
	MaxP99Ms float64 `json:"max_p99_ms,omitempty"`
	// Basis says which figures the thresholds and p99 range used: the rolling
	// window, or run-to-date numbers when this gg build reports no window.
	Basis string `json:"basis"`
}

// subscribe returns a channel of heartbeats and terminal events from now on.
func (s *Session) subscribe() (ch chan *ui.HeartbeatPayload, cancel func()) {
	ch = make(chan *ui.HeartbeatPayload, beatBuffer)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// publishLocked hands an event to every waiter without blocking; s.mu is held.
func (s *Session) publishLocked(ev *ui.HeartbeatPayload) {
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Wait blocks until a new reading meets the watch's thresholds, the wait runs
// out, or the run ends. Only readings that arrive during the wait count, so a
// breach already in the last reading does not end it at once.
func (s *Session) Wait(ctx context.Context, w Watch) (WindowSummary, error) {
	sum := WindowSummary{Basis: "window"}
	if w.Wait <= 0 {
		return sum, errors.New("wait must be positive")
	}
	ch, cancel := s.subscribe()
	defer cancel()

	start := time.Now()
	finish := func(reason string) (WindowSummary, error) {
		sum.WakeReason, sum.WaitedS = reason, round2(time.Since(start).Seconds())
		return sum, nil
	}
	if s.State().Terminal() {
		return finish(WakeRunEnded)
	}

	timer := time.NewTimer(w.Wait)
	defer timer.Stop()
	for {
		select {
		case ev := <-ch:
			if ev.Event != "heartbeat" {
				return finish(WakeRunEnded)
			}
			if reason := sum.observe(ev, w); reason != "" {
				return finish(reason)
			}
		case <-s.done:
			return finish(WakeRunEnded)
		case <-timer.C:
			return finish(WakeTimer)
		case <-ctx.Done():
			return sum, ctx.Err()
		}
	}
}

// observe folds a heartbeat into the summary and returns the wake reason it triggers, if any.
func (m *WindowSummary) observe(ev *ui.HeartbeatPayload, w Watch) string {
	p99, errRate := ev.P99Ms, ev.ErrorRate
	if ev.Window != nil {
		p99, errRate = ev.Window.P99Ms, ev.Window.ErrorRate
	} else {
		m.Basis = "cumulative"
	}

	if m.Beats == 0 {
		m.MinRPS, m.MaxRPS, m.MinP99Ms, m.MaxP99Ms = ev.ActualRPS, ev.ActualRPS, p99, p99
	} else {
		m.MinRPS, m.MaxRPS = min(m.MinRPS, ev.ActualRPS), max(m.MaxRPS, ev.ActualRPS)
		m.MinP99Ms, m.MaxP99Ms = min(m.MinP99Ms, p99), max(m.MaxP99Ms, p99)
	}
	m.Beats++
	m.MinRPS, m.MaxRPS = round2(m.MinRPS), round2(m.MaxRPS)
	m.MinP99Ms, m.MaxP99Ms = round2(m.MinP99Ms), round2(m.MaxP99Ms)

	switch {
	case w.WakeP99Ms > 0 && p99 >= w.WakeP99Ms:
		return WakeP99
	case w.WakeErrRate > 0 && errRate >= w.WakeErrRate:
		return WakeErrorRate
	case w.WakeAnyError && ev.ErrorMix != nil && ev.ErrorMix.Sampled > 0:
		return WakeErrorRate
	}
	return ""
}
