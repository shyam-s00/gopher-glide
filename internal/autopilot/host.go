package autopilot

import (
	"context"
	"fmt"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
)

// Host is a controller running beside an engine, with its output queued on a
// channel for the goroutine that owns the run to read.
type Host struct {
	ctrl   *Controller
	sink   *ChanSink
	cancel context.CancelFunc
	exited chan struct{}
}

// StartHost builds a controller for eng and starts it. Call it before the
// engine starts so a find-ceiling hold is in place for the first window.
func StartHost(eng engine.Runner, cfg *config.Config, p Params) (*Host, error) {
	t, ok := eng.(Target)
	if !ok {
		return nil, fmt.Errorf("this engine cannot be driven by the autopilot")
	}
	ctx, cancel := context.WithCancel(context.Background())
	sink := NewChanSink(ctx)
	ctrl, err := New(p, t, sink, scheduleFor(eng, cfg))
	if err != nil {
		cancel()
		return nil, err
	}
	ctrl.Start(time.Now())
	h := &Host{ctrl: ctrl, sink: sink, cancel: cancel, exited: make(chan struct{})}
	go func() {
		defer close(h.exited)
		ctrl.Run(ctx)
	}()
	return h, nil
}

// scheduleFor reports the stage plan's current rate plus bias.
func scheduleFor(eng engine.Runner, cfg *config.Config) func() (int, bool) {
	return func() (int, bool) {
		at := time.Duration(eng.GetElapsedTime() * float64(time.Second))
		rps := ScheduleAt(cfg.Stages, cfg.ConfigSection.TimeScale, at) + eng.GetBias()
		if rps < 1 {
			rps = 1
		}
		return rps, true
	}
}

// Active reports whether the controller still owns the hold. Safe on nil.
func (h *Host) Active() bool {
	if h == nil {
		return false
	}
	select {
	case <-h.ctrl.Done():
		return false
	default:
		return true
	}
}

// In is the channel the owner of the run reads controller output from; nil
// (never ready) on a nil Host.
func (h *Host) In() <-chan Msg {
	if h == nil {
		return nil
	}
	return h.sink.C
}

// Status is the controller's mode and state; ok is false on a nil Host.
func (h *Host) Status() (mode Mode, state State, ok bool) {
	if h == nil {
		return "", "", false
	}
	mode, state = h.ctrl.Status()
	return mode, state, true
}

// Halt stops the controller and returns anything it had not yet delivered,
// then its closing result. runEnded is true when the run finished on its own.
func (h *Host) Halt(runEnded bool) (Result, []Msg) {
	h.cancel()
	<-h.exited
	var rest []Msg
	for {
		select {
		case m := <-h.sink.C:
			rest = append(rest, m)
		default:
			return h.ctrl.Finalize(runEnded), rest
		}
	}
}
