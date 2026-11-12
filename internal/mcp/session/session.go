// Package session runs gg simulations as supervised child processes driven
// over the Live Control Protocol.
package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/ui"
)

// State is where a session is in its life.
type State string

// Session states. Starting lasts until the first heartbeat arrives.
const (
	Starting State = "starting"
	Running  State = "running"
	Stopping State = "stopping"
	Finished State = "finished"
	Stopped  State = "stopped"
	Failed   State = "failed"
)

// Terminal reports whether no further progress will be made.
func (s State) Terminal() bool { return s == Finished || s == Stopped || s == Failed }

const (
	stderrTailBytes = 4 << 10
	maxEventBytes   = 1 << 20
	maxRecentMarks  = 5
)

// Spec says how to launch one simulation.
type Spec struct {
	Exe  string
	Args []string
	Env  []string // added to the parent's environment
	// Dir holds the run's input files and is removed once the process ends.
	Dir string
	// Planned is how long the run is meant to last; zero if unknown.
	Planned time.Duration
	// PeakRPS is the highest rate the plan reaches; zero if unknown.
	PeakRPS int
}

// Session is one running (or finished) gg child process.
type Session struct {
	ID string

	spec    Spec
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	stderr  *tailBuffer

	startedCh chan struct{} // closed on the first "started" event
	done      chan struct{} // closed once the process exited and its output is drained

	mu          sync.Mutex
	state       State
	t0          time.Time // when "started" arrived: the zero point of elapsed_s
	started     *ui.HeartbeatPayload
	lastBeat    *ui.HeartbeatPayload
	lastBeatAt  time.Time
	marks       []MarkInfo
	terminal    *ui.HeartbeatPayload
	interrupted bool
	snapPath    string
	bias        int // latest cumulative bias reported by the child
	hold        int // pinned rate the child last reported; 0 when none
	subs        map[chan *ui.HeartbeatPayload]struct{}
	biasAckAt   time.Time // when the child last confirmed a bias change
	steers      []SteerInfo
	pending     map[string]chan *ui.HeartbeatPayload
	seq         int
	failure     string
	endedAt     time.Time
	stopping    bool

	steerMu      sync.Mutex // one steering action at a time, so cap checks and writes stay atomic
	replyTimeout time.Duration
	settle       time.Duration
}

// MarkInfo is a mark the child confirmed.
type MarkInfo struct {
	Label    string  `json:"label"`
	ElapsedS float64 `json:"elapsed_s"`
}

func newSession(id string, spec Spec) *Session {
	return &Session{
		ID:        id,
		spec:      spec,
		state:     Starting,
		stderr:    &tailBuffer{max: stderrTailBytes},
		startedCh: make(chan struct{}),
		done:      make(chan struct{}),
		pending:   map[string]chan *ui.HeartbeatPayload{},
		subs:      map[chan *ui.HeartbeatPayload]struct{}{},

		replyTimeout: DefaultReplyTimeout,
		settle:       DefaultSteerSettle,
	}
}

// launch starts the process and the goroutine that reads its output.
func (s *Session) launch() error {
	cmd := exec.Command(s.spec.Exe, s.spec.Args...)
	cmd.Env = append(os.Environ(), s.spec.Env...)
	cmd.Stderr = s.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", s.spec.Exe, err)
	}
	s.cmd, s.stdin = cmd, stdin
	go s.pump(stdout)
	return nil
}

// pump parses the child's NDJSON until it closes stdout, then reaps it.
func (s *Session) pump(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64<<10), maxEventBytes)
	for sc.Scan() {
		var ev ui.HeartbeatPayload
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		s.handle(&ev)
	}
	// An over-long line stops the scanner; keep draining so the child never blocks.
	_, _ = io.Copy(io.Discard, stdout)

	waitErr := s.cmd.Wait()
	s.finish(waitErr)
	close(s.done)
}

func (s *Session) handle(ev *ui.HeartbeatPayload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliverReply(ev)
	switch ev.Event {
	case "started":
		if s.started == nil {
			s.started, s.t0 = ev, time.Now()
			close(s.startedCh)
		}
	case "heartbeat":
		s.lastBeat, s.lastBeatAt = ev, time.Now()
		if ev.Bias != nil {
			s.bias = *ev.Bias
		}
		s.hold = 0
		if ev.HoldRPS != nil {
			s.hold = *ev.HoldRPS
		}
		if s.state == Starting {
			s.state = Running
		}
		s.publishLocked(ev)
	case "mark":
		if ev.ElapsedS != nil {
			s.marks = append(s.marks, MarkInfo{Label: clip(ev.Label, 80), ElapsedS: round2(*ev.ElapsedS)})
			if len(s.marks) > maxRecentMarks {
				s.marks = s.marks[len(s.marks)-maxRecentMarks:]
			}
		}
	case "ack":
		switch {
		case ev.Command == "bias" && ev.Bias != nil:
			s.bias, s.biasAckAt = *ev.Bias, time.Now()
		case ev.Command == "hold" && ev.HoldRPS != nil:
			s.hold = *ev.HoldRPS
		case ev.Command == "release":
			s.hold = 0
		}
	case "interrupted":
		s.interrupted = true
	case "stopped", "finished":
		s.terminal = ev
		s.snapPath = snapshotPath(ev.Message)
		s.endedAt = time.Now()
		switch {
		case ev.Event == "stopped" || s.interrupted || s.stopping:
			s.state = Stopped
		default:
			s.state = Finished
		}
		s.publishLocked(ev)
	}
}

// finish runs once the process is gone: settle the state and clean up.
func (s *Session) finish(waitErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endedAt.IsZero() {
		s.endedAt = time.Now()
	}
	if s.terminal == nil {
		s.state = Failed
		s.failure = exitReason(waitErr)
	}
	if s.spec.Dir != "" {
		_ = os.RemoveAll(s.spec.Dir)
	}
}

func exitReason(err error) string {
	if err == nil {
		return "process exited before reporting a result"
	}
	return "process ended without a result: " + err.Error()
}

// waitStarted blocks until the child reports "started", exits, or the wait is cut short.
func (s *Session) waitStarted(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.startedCh:
		return nil
	case <-s.done:
		return s.startupFailure()
	case <-timer.C:
		s.kill()
		return fmt.Errorf("gg did not start within %s", timeout)
	case <-ctx.Done():
		s.kill()
		return ctx.Err()
	}
}

func (s *Session) startupFailure() error {
	msg := "gg exited before the run started"
	if tail := strings.TrimSpace(dropBanner(s.stderr.String())); tail != "" {
		msg += ": " + lastLines(tail, 6)
	}
	return errors.New(msg)
}

// Done is closed when the process has exited and all its output was read.
func (s *Session) Done() <-chan struct{} { return s.done }

// State returns the current state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// WriteLine sends one LCP command line to the child. It refuses once the
// session is over, so nothing is written to a closed pipe.
func (s *Session) WriteLine(line string) error {
	if s.State().Terminal() {
		return fmt.Errorf("session %s is %s", s.ID, s.State())
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := io.WriteString(s.stdin, strings.TrimRight(line, "\n")+"\n")
	return err
}

// StopOptions bound how long Stop waits before escalating.
type StopOptions struct {
	// Graceful is how long the child gets to stop on its own.
	Graceful time.Duration
	// Term is how long it gets after SIGTERM before it is killed.
	Term time.Duration
}

// Stop asks the child to stop, waits for it to finish its snapshot and exit,
// and escalates to SIGTERM and then kill if it does not. It is safe to call
// concurrently and on a session that already ended.
func (s *Session) Stop(ctx context.Context, o StopOptions) {
	s.mu.Lock()
	first := !s.stopping && !s.state.Terminal()
	if first {
		s.stopping = true
		s.state = Stopping
	}
	s.mu.Unlock()

	if first {
		_ = s.sendStop()
	}
	if s.waitDone(ctx, o.Graceful) {
		return
	}
	_ = terminate(s.cmd.Process)
	if s.waitDone(ctx, o.Term) {
		return
	}
	s.kill()
	s.waitDone(context.Background(), 5*time.Second)
}

func (s *Session) sendStop() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := io.WriteString(s.stdin, `{"id":"stop","command":"stop"}`+"\n")
	return err
}

func (s *Session) waitDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.done:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (s *Session) kill() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

// terminate asks the process to exit; Windows has no SIGTERM, so it is killed.
func terminate(p *os.Process) error {
	if p == nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		return p.Kill()
	}
	return p.Signal(syscall.SIGTERM)
}

// snapshotPath extracts the path from the "Snapshot saved → <path>" status line.
func snapshotPath(msg string) string {
	_, rest, ok := strings.Cut(msg, "Snapshot saved → ")
	if !ok {
		return ""
	}
	if i := strings.Index(rest, "  "); i >= 0 {
		rest = rest[:i]
	}
	return strings.TrimSpace(rest)
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(bytes.ToValidUTF8(t.buf, nil))
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// dropBanner removes gg's own start-up chatter so the real error stands out.
func dropBanner(stderr string) string {
	var keep []string
	for _, l := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(l, "gg (Gopher Glide)") || strings.TrimSpace(l) == "Starting..." {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}
