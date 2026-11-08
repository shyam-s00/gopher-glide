// Package fakegg is a scripted stand-in for the gg child process, for tests.
package fakegg

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Env names the environment variable that selects the scenario.
const Env = "GG_FAKE_CHILD"

func emit(v map[string]any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func emitStarted(scenario string) {
	caps := []string{"control.bias", "control.mark", "control.mark.elapsed", "control.stop"}
	if scenario == "no-elapsed-cap" {
		caps = []string{"control.bias", "control.mark", "control.stop"}
	}
	emit(map[string]any{
		"event": "started", "total_stages": 2, "protocol_version": 1,
		"capabilities": caps,
		"stages": []map[string]any{
			{"name": "Ramp Up", "duration_seconds": 5, "target_rps": 10},
			{"name": "Sustain", "duration_seconds": 5, "target_rps": 10},
		},
	})
}

// emitBeat reports the target with the bias the "queen" has applied so far and
// the live cumulative bias, like the real engine.
func emitBeat(n, applied, live int) {
	target := 10 + applied
	if target < 1 {
		target = 1
	}
	emit(map[string]any{
		"event": "heartbeat", "stage": 1, "total_stages": 2, "target_rps": target, "actual_rps": 9.87654,
		"total_requests": n * 10, "success_count": n*10 - n, "failure_count": n, "error_rate": 0.1,
		"p50_ms": 12.345, "p95_ms": 30.1, "p99_ms": 55.5, "bias": live,
		"error_mix": map[string]any{"window_s": 10, "sampled": n, "http_5xx": n, "capped": false,
			"top_codes": []map[string]any{{"code": 503, "n": n}}},
	})
}

// SnapEnv, when set, is the snapshot path the fake reports in its final event.
const SnapEnv = "GG_FAKE_SNAP"

func emitStopped() {
	path := "/tmp/gg snaps/run-1.snap"
	if p := os.Getenv(SnapEnv); p != "" {
		path = p
	}
	emit(map[string]any{
		"event": "stopped", "total_requests": 100, "success_count": 90, "failure_count": 10,
		"error_rate": 0.1, "p50_ms": 12.3, "p95_ms": 30.1, "p99_ms": 55.5,
		"message": "✓ Snapshot saved → " + path + "  ⚠ 3 dropped",
	})
}

// stdinLines delivers the lines of stdin and closes the channel at EOF.
func stdinLines() <-chan string {
	ch := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			ch <- sc.Text()
		}
		close(ch)
	}()
	return ch
}

// Run plays the named scenario as a stand-in for `gg --headless --reporter json`,
// reading LCP commands on stdin and writing events to stdout. It returns when the
// scenario ends, or exits the process for the failure scenarios.
func Run(scenario string) {
	switch scenario {
	case "no-started":
		fmt.Fprintln(os.Stderr, "Error: invalid config: no stages defined")
		os.Exit(1)
	case "slow-start":
		time.Sleep(30 * time.Second)
		return
	case "die-after-start":
		emitStarted(scenario)
		emitBeat(1, 0, 0)
		fmt.Fprintln(os.Stderr, strings.Repeat("noise line\n", 600)+"panic: boom")
		os.Exit(2)
	case "no-heartbeat":
		emitStarted(scenario)
		for range stdinLines() {
		}
		return
	case "finish-self":
		emitStarted(scenario)
		emitBeat(1, 0, 0)
		time.Sleep(100 * time.Millisecond)
		emit(map[string]any{"event": "finished", "total_requests": 50, "success_count": 50, "error_rate": 0, "message": "Load test completed"})
		return
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
	}

	emitStarted(scenario)
	start := time.Now()
	applied, live := 0, 0
	lines := stdinLines()
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	for n := 1; ; {
		select {
		case <-tick.C:
			applied = live
			emitBeat(n, applied, live)
			n++
		case line, ok := <-lines:
			if !ok { // stdin closed: the parent is gone
				if scenario == "eof-exit" {
					emitStopped()
					return
				}
				lines = nil
				continue
			}
			var cmd struct {
				ID, Command, Label string
				Amount             int
				ElapsedS           *float64 `json:"elapsed_s"`
			}
			_ = json.Unmarshal([]byte(line), &cmd)
			switch {
			case cmd.Command == "stop" && (scenario == "ignore-stop" || scenario == "ignore-term"):
			case cmd.Command == "stop":
				emitStopped()
				return
			case (cmd.Command == "mark" || cmd.Command == "bias") && scenario == "no-reply":
			case cmd.Command == "bias" && scenario == "reject-bias":
				emit(map[string]any{"event": "error", "id": cmd.ID, "command": "bias", "reason": "invalid_argument", "message": "amount must be a non-zero integer"})
			case cmd.Command == "bias":
				if scenario == "slow-ack" {
					time.Sleep(40 * time.Millisecond)
				}
				live += cmd.Amount
				emit(map[string]any{"event": "ack", "id": cmd.ID, "command": "bias", "bias": live,
					"message": fmt.Sprintf("bias %+d applied (cumulative %+d)", cmd.Amount, live)})
			case cmd.Command == "mark":
				now := time.Since(start).Seconds()
				at, clamped := 1.5, false
				if cmd.ElapsedS != nil && scenario != "no-elapsed-cap" {
					at = *cmd.ElapsedS
					if scenario == "clamp" && at > now {
						at, clamped = now, true
					}
				} else if scenario == "clamp" || scenario == "no-elapsed-cap" {
					at = now
				}
				ev := map[string]any{"event": "mark", "id": cmd.ID, "label": cmd.Label, "elapsed_s": at}
				if clamped {
					ev["clamped"] = true
				}
				emit(ev)
			}
		}
	}
}
