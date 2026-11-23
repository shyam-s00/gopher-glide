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
	caps := []string{"control.bias", "control.mark", "control.mark.elapsed", "control.stop", "control.hold", "control.autopilot"}
	switch scenario {
	case "no-autopilot-cap":
		caps = []string{"control.bias", "control.mark", "control.mark.elapsed", "control.stop", "control.hold"}
	case "no-elapsed-cap":
		caps = []string{"control.bias", "control.mark", "control.stop"}
	case "no-hold-cap":
		caps = []string{"control.bias", "control.mark", "control.mark.elapsed", "control.stop"}
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

// DegradeAfter is the heartbeat number from which the "degrade" scenario's
// windowed figures turn bad (p99 300 ms, 20% errors, failures in the mix).
const DegradeAfter = 4

// emitBeat reports the target with the bias the "queen" has applied so far and
// the live cumulative bias, like the real engine. A non-zero pinned rate is a hold.
func emitBeat(scenario string, n, applied, live, pinned int) {
	target := 10 + applied
	if target < 1 {
		target = 1
	}
	if pinned > 0 {
		target = pinned
	}
	winP99, winErr, failing := 20.0, 0.0, n
	if scenario == "degrade" {
		failing = 0
		if n >= DegradeAfter {
			winP99, winErr, failing = 300, 0.2, n
		}
	}
	beat := map[string]any{
		"event": "heartbeat", "stage": 1, "total_stages": 2, "target_rps": target, "actual_rps": 9.87654,
		"total_requests": n * 10, "success_count": n*10 - n, "failure_count": n, "error_rate": 0.1,
		"p50_ms": 12.345, "p95_ms": 30.1, "p99_ms": 55.5, "bias": live,
		"elapsed_s": float64(n) * 0.03,
	}
	if scenario != "no-window" {
		beat["window"] = map[string]any{"seconds": 5, "p99_ms": winP99, "error_rate": winErr, "samples": 100}
	}
	if pinned > 0 {
		beat["hold_rps"] = pinned
	}
	if apMode != "" {
		beat["autopilot"] = map[string]any{"mode": apMode, "state": "watching"}
	}
	if failing > 0 {
		beat["error_mix"] = map[string]any{"window_s": 10, "sampled": failing, "http_5xx": failing, "capped": false,
			"top_codes": []map[string]any{{"code": 503, "n": failing}}}
	}
	emit(beat)
}

// apMode is the controller the fake is running ("" when none); heartbeats carry it.
var apMode string

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
		emitBeat(scenario, 1, 0, 0, 0)
		fmt.Fprintln(os.Stderr, strings.Repeat("noise line\n", 600)+"panic: boom")
		os.Exit(2)
	case "no-heartbeat":
		emitStarted(scenario)
		for range stdinLines() {
		}
		return
	case "finish-self":
		emitStarted(scenario)
		emitBeat(scenario, 1, 0, 0, 0)
		time.Sleep(100 * time.Millisecond)
		emit(map[string]any{"event": "finished", "total_requests": 50, "success_count": 50, "error_rate": 0, "message": "Load test completed"})
		return
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
	}

	emitStarted(scenario)
	apMode = ""
	if scenario == "autopilot-ceiling" {
		apMode = "find-ceiling"
	}
	start := time.Now()
	applied, live, pinned := 0, 0, 0
	lines := stdinLines()
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	for n := 1; ; {
		select {
		case <-tick.C:
			applied = live
			emitBeat(scenario, n, applied, live, pinned)
			n++
			if scenario == "autopilot-ceiling" && n == 8 {
				emit(map[string]any{"event": "mark", "label": "Autopilot: ceiling breached at 110 RPS", "elapsed_s": 3.0, "source": "autopilot"})
				emit(map[string]any{"event": "autopilot", "mode": "find-ceiling", "state": "done", "hold_rps": 95, "ceiling_rps": 95,
					"outcome": "ceiling_found", "reason": "ceiling confirmed at 95 RPS"})
				emit(map[string]any{"event": "finished", "total_requests": 50, "success_count": 50, "error_rate": 0,
					"mode": "find-ceiling", "outcome": "ceiling_found", "ceiling_rps": 95, "message": "Load test completed"})
				return
			}
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
				Action, Mode       string
				Amount, RPS        int
				Source             string
				ElapsedS           *float64 `json:"elapsed_s"`
			}
			_ = json.Unmarshal([]byte(line), &cmd)
			needsAgent := cmd.Command == "bias" || cmd.Command == "mark" || cmd.Command == "hold" || cmd.Command == "release"
			switch {
			case apMode != "" && (cmd.Command == "bias" || cmd.Command == "hold" || cmd.Command == "release"):
				emit(map[string]any{"event": "error", "id": cmd.ID, "command": cmd.Command, "reason": "autopilot_active", "message": "autopilot owns the rate"})
			case cmd.Command == "autopilot" && cmd.Action == "start":
				apMode = cmd.Mode
				pinned = 7
				emit(map[string]any{"event": "ack", "id": cmd.ID, "command": "autopilot", "mode": cmd.Mode})
				emit(map[string]any{"event": "autopilot", "mode": cmd.Mode, "state": "watching", "reason": "watching"})
			case cmd.Command == "autopilot" && cmd.Action == "stop":
				emit(map[string]any{"event": "autopilot", "mode": apMode, "state": "done", "outcome": "stopped", "hold_rps": pinned})
				apMode = ""
				emit(map[string]any{"event": "ack", "id": cmd.ID, "command": "autopilot"})
			case needsAgent && scenario == "require-agent-source" && cmd.Source != "agent":
				emit(map[string]any{"event": "error", "id": cmd.ID, "command": cmd.Command, "reason": "invalid_argument", "message": "source must be agent"})
			case cmd.Command == "stop" && (scenario == "ignore-stop" || scenario == "ignore-term"):
			case cmd.Command == "stop":
				emit(map[string]any{"event": "ack", "id": cmd.ID, "command": "stop", "message": "stop received"})
				emitStopped()
				return
			case (cmd.Command == "mark" || cmd.Command == "bias" || cmd.Command == "hold") && scenario == "no-reply":
			case cmd.Command == "bias" && scenario == "reject-bias":
				emit(map[string]any{"event": "error", "id": cmd.ID, "command": "bias", "reason": "invalid_argument", "message": "amount must be a non-zero integer"})
			case cmd.Command == "hold" && scenario == "reject-hold":
				emit(map[string]any{"event": "error", "id": cmd.ID, "command": "hold", "reason": "invalid_argument", "message": "rps must be an integer >= 1"})
			case cmd.Command == "hold":
				pinned = cmd.RPS
				emit(map[string]any{"event": "ack", "id": cmd.ID, "command": "hold", "hold_rps": pinned, "source": cmd.Source})
			case cmd.Command == "release":
				pinned = 0
				emit(map[string]any{"event": "ack", "id": cmd.ID, "command": "release", "source": cmd.Source})
			case cmd.Command == "bias":
				if scenario == "slow-ack" {
					time.Sleep(40 * time.Millisecond)
				}
				live += cmd.Amount
				emit(map[string]any{"event": "ack", "id": cmd.ID, "command": "bias", "bias": live, "source": cmd.Source,
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
				ev := map[string]any{"event": "mark", "id": cmd.ID, "label": cmd.Label, "elapsed_s": at, "source": cmd.Source}
				if clamped {
					ev["clamped"] = true
				}
				emit(ev)
			}
		}
	}
}
