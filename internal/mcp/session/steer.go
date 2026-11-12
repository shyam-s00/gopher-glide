package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/ui"
)

// Steering defaults.
const (
	DefaultReplyTimeout = 5 * time.Second
	// DefaultSteerSettle is a little over the engine's 1 s window: a heartbeat
	// older than this after a bias change already includes it.
	DefaultSteerSettle = 1250 * time.Millisecond
	maxRecentSteers    = 5
	maxLabelBytes      = 200
)

// NotRunningError is returned when a command targets a session that is stopping or over.
type NotRunningError struct {
	ID    string
	State State
}

func (e *NotRunningError) Error() string {
	return fmt.Sprintf("session_not_running: %s is %s; steering only works on a running simulation", e.ID, e.State)
}

// ReplyError is a command gg understood but refused.
type ReplyError struct{ Reason, Message string }

func (e *ReplyError) Error() string {
	return "gg rejected the command (" + e.Reason + "): " + e.Message
}

// SteerInfo is a rate change that gg confirmed.
type SteerInfo struct {
	Action    string  `json:"action"`
	Delta     int     `json:"delta"`
	BiasTotal int     `json:"bias_total"`
	HoldRPS   int     `json:"hold_rps,omitempty"`
	AtS       float64 `json:"at_s"`
}

// Reading is what a planner may use to decide a bias change.
type Reading struct {
	// Available is false until the first heartbeat arrives.
	Available bool
	// TargetRPS is the target in the latest heartbeat. It lags bias changes by up to a window.
	TargetRPS int
	// BiasAtReading is the bias the latest heartbeat reported.
	BiasAtReading int
	// Bias is the newest cumulative bias gg has confirmed.
	Bias int
	// Settled is true when the heartbeat is newer than every bias change by a full window.
	Settled bool
	// Age is how old the heartbeat is.
	Age time.Duration
	// PeakRPS is the plan's highest rate; zero if unknown.
	PeakRPS int
	// HoldRPS is the rate pinned by a hold; zero when none.
	HoldRPS int
}

// Planner turns a reading into the bias delta to send. A zero delta sends nothing.
type Planner func(Reading) (delta int, note string, err error)

// BiasResult is the outcome of a bias change.
type BiasResult struct {
	Delta     int
	BiasTotal int
	// TargetBefore is the target in the reading the change was based on; zero if none.
	TargetBefore int
	Note         string
}

// MarkResult is the outcome of a mark.
type MarkResult struct {
	Label    string
	ElapsedS float64
	Clamped  bool
	// ClientOffsetUsed is false when the offset was dropped because gg cannot honour it.
	ClientOffsetUsed bool
	Warning          string
}

// reading snapshots what planners need.
func (s *Session) reading() Reading {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := Reading{Bias: s.bias, PeakRPS: s.spec.PeakRPS, HoldRPS: s.hold, Settled: true}
	if s.lastBeat == nil {
		return r
	}
	r.Available, r.TargetRPS, r.Age = true, s.lastBeat.TargetRPS, time.Since(s.lastBeatAt)
	if s.lastBeat.Bias != nil {
		r.BiasAtReading = *s.lastBeat.Bias
	}
	if !s.biasAckAt.IsZero() {
		r.Settled = s.lastBeatAt.Sub(s.biasAckAt) >= s.settle
	}
	return r
}

// SteerBias plans and sends one bias change. Planning and sending happen
// under one lock so concurrent callers cannot both pass a cap check.
func (s *Session) SteerBias(ctx context.Context, action string, plan Planner) (BiasResult, error) {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if err := s.checkRunning(); err != nil {
		return BiasResult{}, err
	}

	r := s.reading()
	delta, note, err := plan(r)
	if err != nil {
		return BiasResult{}, err
	}
	res := BiasResult{Delta: delta, BiasTotal: r.Bias, Note: note}
	if r.Available {
		res.TargetBefore = r.TargetRPS
	}
	if delta == 0 {
		return res, nil
	}

	reply, err := s.request(ctx, map[string]any{"command": "bias", "amount": delta, "source": SourceAgent})
	if err != nil {
		return BiasResult{}, err
	}
	if reply.Bias != nil {
		res.BiasTotal = *reply.Bias
	}
	s.recordSteer(SteerInfo{Action: action, Delta: delta, BiasTotal: res.BiasTotal})
	return res, nil
}

// SourceAgent tags every command this server sends on an agent's behalf.
const SourceAgent = "agent"

// HoldResult is the outcome of a hold or release.
type HoldResult struct {
	// HoldRPS is the pinned rate; zero after a release.
	HoldRPS int
}

// ErrNoHold is returned when gg cannot pin the rate.
var ErrNoHold = errors.New("this gg build cannot hold the rate (it does not advertise control.hold); use adjust_rps or set_rps")

// Hold pins the target rate at rps until released. The server's rate cap is the
// caller's to check; this only sends the command.
func (s *Session) Hold(ctx context.Context, rps int) (HoldResult, error) {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if err := s.checkRunning(); err != nil {
		return HoldResult{}, err
	}
	if !s.hasCapability("control.hold") {
		return HoldResult{}, ErrNoHold
	}
	reply, err := s.request(ctx, map[string]any{"command": "hold", "rps": rps, "source": SourceAgent})
	if err != nil {
		return HoldResult{}, err
	}
	res := HoldResult{HoldRPS: rps}
	if reply.HoldRPS != nil {
		res.HoldRPS = *reply.HoldRPS
	}
	s.recordSteer(SteerInfo{Action: "hold_rps", HoldRPS: res.HoldRPS})
	return res, nil
}

// Release returns the run to its profile schedule plus bias.
func (s *Session) Release(ctx context.Context) error {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if err := s.checkRunning(); err != nil {
		return err
	}
	if !s.hasCapability("control.hold") {
		return ErrNoHold
	}
	if _, err := s.request(ctx, map[string]any{"command": "release", "source": SourceAgent}); err != nil {
		return err
	}
	s.recordSteer(SteerInfo{Action: "release_hold"})
	return nil
}

// Mark drops a labelled marker. atElapsed is a run-relative offset in seconds
// (as in telemetry's elapsed_s); it is sent only if gg can honour it.
func (s *Session) Mark(ctx context.Context, label string, atElapsed *float64) (MarkResult, error) {
	if label == "" || len(label) > maxLabelBytes {
		return MarkResult{}, fmt.Errorf("label must be 1 to %d bytes", maxLabelBytes)
	}
	if atElapsed != nil && (*atElapsed < 0 || math.IsNaN(*atElapsed) || math.IsInf(*atElapsed, 0)) {
		return MarkResult{}, errors.New("at_elapsed_s must be zero or more")
	}
	if err := s.checkRunning(); err != nil {
		return MarkResult{}, err
	}

	cmd := map[string]any{"command": "mark", "label": label, "source": SourceAgent}
	res := MarkResult{Label: label}
	if atElapsed != nil {
		if s.hasCapability("control.mark.elapsed") {
			cmd["elapsed_s"], res.ClientOffsetUsed = *atElapsed, true
		} else {
			res.Warning = "this gg build cannot take a client time for marks, so the mark was stamped with the time gg received it"
		}
	}
	reply, err := s.request(ctx, cmd)
	if err != nil {
		return MarkResult{}, err
	}
	if reply.ElapsedS != nil {
		res.ElapsedS = round2(*reply.ElapsedS)
	}
	res.Clamped = reply.Clamped
	if res.Clamped {
		res.Warning = "at_elapsed_s was ahead of the run, so the mark was placed at the current time"
	}
	return res, nil
}

func (s *Session) hasCapability(name string) bool {
	_, caps, _, _ := s.Started()
	return slices.Contains(caps, name)
}

func (s *Session) checkRunning() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runningErrLocked()
}

func (s *Session) runningErrLocked() error {
	if s.state.Terminal() || s.stopping {
		return &NotRunningError{ID: s.ID, State: s.state}
	}
	return nil
}

func (s *Session) recordSteer(st SteerInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.t0.IsZero() {
		st.AtS = round2(time.Since(s.t0).Seconds())
	}
	s.steers = append(s.steers, st)
	if len(s.steers) > maxRecentSteers {
		s.steers = s.steers[len(s.steers)-maxRecentSteers:]
	}
}

// request sends one command with a fresh id and waits for the reply carrying
// that id. Writes are serialized; replies are matched by id, so concurrent
// callers each get their own.
func (s *Session) request(ctx context.Context, cmd map[string]any) (*ui.HeartbeatPayload, error) {
	s.mu.Lock()
	if err := s.runningErrLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.seq++
	id := "c" + strconv.Itoa(s.seq)
	ch := make(chan *ui.HeartbeatPayload, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	defer s.forget(id)

	cmd["id"] = id
	line, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	if err := s.writeCommand(line); err != nil {
		return nil, err
	}

	timer := time.NewTimer(s.replyTimeout)
	defer timer.Stop()
	select {
	case reply := <-ch:
		return reply, replyErr(reply)
	case <-s.done:
		select {
		case reply := <-ch:
			return reply, replyErr(reply)
		default:
			return nil, &NotRunningError{ID: s.ID, State: s.State()}
		}
	case <-timer.C:
		return nil, fmt.Errorf("gg did not reply within %s; the command may or may not have been applied, so check get_live_telemetry before retrying", s.replyTimeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Session) writeCommand(line []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write(append(line, '\n')); err != nil {
		select {
		case <-s.done:
			return &NotRunningError{ID: s.ID, State: s.State()}
		default:
		}
		if errors.Is(err, io.ErrClosedPipe) {
			return &NotRunningError{ID: s.ID, State: s.State()}
		}
		return fmt.Errorf("write to gg: %w", err)
	}
	return nil
}

func replyErr(r *ui.HeartbeatPayload) error {
	if r.Event == "error" {
		return &ReplyError{Reason: r.Reason, Message: r.Message}
	}
	return nil
}

func (s *Session) forget(id string) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

// deliverReply hands a reply to the caller waiting on its id; the caller holds s.mu.
func (s *Session) deliverReply(ev *ui.HeartbeatPayload) {
	if ev.ID == "" || (ev.Event != "ack" && ev.Event != "mark" && ev.Event != "error") {
		return
	}
	if ch, ok := s.pending[ev.ID]; ok {
		select {
		case ch <- ev:
		default:
		}
	}
}
