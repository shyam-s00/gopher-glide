package ui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/autopilot"
)

// cmdInputClosed is an internal pseudo-command, never parsed from the wire: the
// control input reached EOF and the renderer was asked to stop on that.
const cmdInputClosed = "\x00input-closed"

// controlLineMaxBytes caps a single control command line — a deliberate
// protocol limit, not an incidental one.
const controlLineMaxBytes = 64 * 1024

// controlCommand is one parsed, validated command from the stdin control
// protocol (LCP), or a failed one carrying the error to reply with instead.
type controlCommand struct {
	ID      string // caller-chosen correlation id; empty if omitted
	Command string // "bias" | "mark" | "stop" | "hold" | "release" | "autopilot"
	Amount  int    // bias only: signed, non-zero delta
	Label   string // mark only: non-empty annotation text
	RPS     int    // hold only: pinned rate, >= 1

	// Source says who issued a bias, hold, release or mark; empty when the
	// client sent none.
	Source string

	// Autopilot is set for the "autopilot" command only.
	Autopilot *autopilotCommand

	// ElapsedS is a mark's client-captured run-relative offset; nil when the
	// client sent none (the server then stamps its own receive time).
	ElapsedS *float64

	// Err is set when the line failed to parse or validate. Every other
	// field is zero; the select loop should reply with Err, not execute
	// anything.
	Err *controlError
}

// autopilotCommand is a parsed "autopilot" start or stop request.
type autopilotCommand struct {
	Action string // "start" | "stop"
	Params autopilot.Params
}

// controlError is a typed command-line failure, carrying the error
// reason plus whatever of id/command survived parsing.
type controlError struct {
	Reason  string // parse_error | unknown_command | invalid_argument
	ID      string // set for unknown_command / invalid_argument only
	Command string // set for unknown_command / invalid_argument only
	Message string
}

func (e *controlError) Error() string { return e.Message }

// rawControlCommand keeps Amount/Label/ElapsedS raw so a bad type there doesn't
// fail decoding — only a non-string id/command, or broken JSON, is parse_error.
// Those fields are validated separately, into invalid_argument.
type rawControlCommand struct {
	ID       string          `json:"id"`
	Command  string          `json:"command"`
	Amount   json.RawMessage `json:"amount"`
	Label    json.RawMessage `json:"label"`
	ElapsedS json.RawMessage `json:"elapsed_s"`
	RPS      json.RawMessage `json:"rps"`
	Source   json.RawMessage `json:"source"`

	Action json.RawMessage `json:"action"`
	Mode   json.RawMessage `json:"mode"`
	// Autopilot tuning, all optional; absent or 0 means the default.
	MaxP99Ms      json.RawMessage `json:"max_p99_ms"`
	MaxErrorRate  json.RawMessage `json:"max_error_rate"`
	MinSamples    json.RawMessage `json:"min_samples"`
	StartRPS      json.RawMessage `json:"start_rps"`
	StepPct       json.RawMessage `json:"step_pct"`
	StepRPS       json.RawMessage `json:"step_rps"`
	StepIntervalS json.RawMessage `json:"step_interval_s"`
	ConfirmHoldS  json.RawMessage `json:"confirm_hold_s"`
	MaxRPS        json.RawMessage `json:"max_rps"`
	MaxDurationS  json.RawMessage `json:"max_duration_s"`
	RecoverHoldS  json.RawMessage `json:"recover_hold_s"`
}

// parseControlLine decodes and validates one NDJSON command line, returning
// either a ready-to-execute controlCommand or a typed *controlError.
func parseControlLine(line []byte) (controlCommand, *controlError) {
	var raw rawControlCommand
	if err := json.Unmarshal(line, &raw); err != nil {
		// id/command are non-string or the line isn't JSON at all — neither
		// is recoverable, so the reply carries no id/command.
		return controlCommand{}, &controlError{
			Reason:  "parse_error",
			Message: fmt.Sprintf("invalid command line: %v", err),
		}
	}

	switch raw.Command {
	case "bias", "mark", "hold", "release":
		if _, ok := decodeSource(raw.Source); !ok {
			return controlCommand{}, &controlError{
				Reason: "invalid_argument", ID: raw.ID, Command: raw.Command,
				Message: "source must be one of: user, agent, autopilot",
			}
		}
	}
	source, _ := decodeSource(raw.Source)

	switch raw.Command {
	case "bias":
		amount, ok := decodeInt(raw.Amount)
		if !ok || amount == 0 {
			return controlCommand{}, &controlError{
				Reason: "invalid_argument", ID: raw.ID, Command: raw.Command,
				Message: "amount must be a non-zero integer",
			}
		}
		return controlCommand{ID: raw.ID, Command: raw.Command, Amount: amount, Source: source}, nil

	case "mark":
		label, ok := decodeString(raw.Label)
		if !ok || label == "" {
			return controlCommand{}, &controlError{
				Reason: "invalid_argument", ID: raw.ID, Command: raw.Command,
				Message: "label must be a non-empty string",
			}
		}
		elapsed, ok := decodeOptionalOffset(raw.ElapsedS)
		if !ok {
			return controlCommand{}, &controlError{
				Reason: "invalid_argument", ID: raw.ID, Command: raw.Command,
				Message: "elapsed_s must be a non-negative number",
			}
		}
		return controlCommand{ID: raw.ID, Command: raw.Command, Label: label, ElapsedS: elapsed, Source: source}, nil

	case "hold":
		rps, ok := decodeInt(raw.RPS)
		if !ok || rps < 1 {
			return controlCommand{}, &controlError{
				Reason: "invalid_argument", ID: raw.ID, Command: raw.Command,
				Message: "rps must be an integer >= 1",
			}
		}
		return controlCommand{ID: raw.ID, Command: raw.Command, RPS: rps, Source: source}, nil

	case "release":
		return controlCommand{ID: raw.ID, Command: raw.Command, Source: source}, nil

	case "stop":
		return controlCommand{ID: raw.ID, Command: raw.Command}, nil

	case "autopilot":
		return parseAutopilot(raw)

	default:
		return controlCommand{}, &controlError{
			Reason: "unknown_command", ID: raw.ID, Command: raw.Command,
			Message: fmt.Sprintf("unknown command %q", raw.Command),
		}
	}
}

// parseAutopilot validates the shape of an "autopilot" command; value ranges
// are checked when the controller is built.
func parseAutopilot(raw rawControlCommand) (controlCommand, *controlError) {
	bad := func(msg string) (controlCommand, *controlError) {
		return controlCommand{}, &controlError{Reason: "invalid_argument", ID: raw.ID, Command: raw.Command, Message: msg}
	}
	action, _ := decodeString(raw.Action)
	switch action {
	case "stop":
		return controlCommand{ID: raw.ID, Command: raw.Command, Autopilot: &autopilotCommand{Action: "stop"}}, nil
	case "start":
	default:
		return bad("action must be \"start\" or \"stop\"")
	}
	mode, _ := decodeString(raw.Mode)
	if mode != string(autopilot.ModeFindCeiling) && mode != string(autopilot.ModeSLOGuard) {
		return bad("mode must be \"find-ceiling\" or \"slo-guard\"")
	}

	p := autopilot.Params{Mode: autopilot.Mode(mode)}
	nums := []struct {
		raw  json.RawMessage
		name string
		set  func(float64)
	}{
		{raw.MaxP99Ms, "max_p99_ms", func(v float64) { p.MaxP99Ms = v }},
		{raw.MaxErrorRate, "max_error_rate", func(v float64) { p.MaxErrorRate = v }},
		{raw.MinSamples, "min_samples", func(v float64) { p.MinSamples = int(v) }},
		{raw.StartRPS, "start_rps", func(v float64) { p.StartRPS = int(v) }},
		{raw.StepPct, "step_pct", func(v float64) { p.StepPct = int(v) }},
		{raw.StepRPS, "step_rps", func(v float64) { p.StepRPS = int(v) }},
		{raw.StepIntervalS, "step_interval_s", func(v float64) { p.StepInterval = secs(v) }},
		{raw.ConfirmHoldS, "confirm_hold_s", func(v float64) { p.ConfirmHold = secs(v) }},
		{raw.MaxRPS, "max_rps", func(v float64) { p.MaxRPS = int(v) }},
		{raw.MaxDurationS, "max_duration_s", func(v float64) { p.MaxDuration = secs(v) }},
		{raw.RecoverHoldS, "recover_hold_s", func(v float64) { p.RecoverHold = secs(v) }},
	}
	for _, n := range nums {
		v, ok := decodeOptionalOffset(n.raw)
		if !ok {
			return bad(n.name + " must be a non-negative number")
		}
		if v != nil {
			n.set(*v)
		}
	}
	return controlCommand{ID: raw.ID, Command: raw.Command, Autopilot: &autopilotCommand{Action: "start", Params: p}}, nil
}

func secs(v float64) time.Duration { return time.Duration(v * float64(time.Second)) }

// decodeInt reports whether raw holds a valid JSON integer. An absent field
// and a present-but-wrong-typed one both return ok=false so callers treat
// them the same way (invalid_argument).
func decodeInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	return n, true
}

// decodeString reports whether raw holds a valid JSON string, with the same
// absent-vs-wrong-typed handling as decodeInt.
func decodeString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// decodeSource parses the optional "source" field. Absent and null mean none
// (empty, ok); otherwise it must be one of the known values.
func decodeSource(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	switch s {
	case "user", "agent", "autopilot":
		return s, true
	}
	return "", false
}

// decodeOptionalOffset parses an optional non-negative JSON number. Absent and
// null both mean "not provided" (nil, ok); anything else non-numeric or
// negative is ok=false.
func decodeOptionalOffset(raw json.RawMessage) (*float64, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil || v < 0 {
		return nil, false
	}
	if v == 0 {
		v = 0 // normalise -0 so it never serialises as "-0"
	}
	return &v, true
}

// controlReader reads r line by line into cmdCh — parsing only, not
// executing. Sends block rather than drop on a full channel (every
// command needs a reply); it never closes cmdCh, since EOF isn't a stop.
func controlReader(r io.Reader, cmdCh chan<- controlCommand) {
	br := bufio.NewReaderSize(r, controlLineMaxBytes+2) // +2: content up to the cap, plus a CRLF terminator
	for {
		line, tooLong, err := readControlLine(br)
		if tooLong {
			cmdCh <- controlCommand{Err: &controlError{
				Reason:  "parse_error",
				Message: fmt.Sprintf("line exceeds %d byte limit", controlLineMaxBytes),
			}}
		} else if len(line) > 0 {
			cmd, cerr := parseControlLine(line)
			if cerr != nil {
				cmdCh <- controlCommand{Err: cerr}
			} else {
				cmdCh <- cmd
			}
		}
		if err != nil {
			if err != io.EOF {
				cmdCh <- controlCommand{Err: &controlError{
					Reason:  "parse_error",
					Message: fmt.Sprintf("control stream read error: %v", err),
				}}
			}
			return
		}
	}
}

// readControlLine returns one \n-terminated line (trailing \r trimmed). A
// line over the buffer's capacity sets tooLong and is discarded up to its
// next newline (or EOF), so the caller resyncs cleanly on the line after it.
func readControlLine(br *bufio.Reader) (line []byte, tooLong bool, err error) {
	for {
		line, err = br.ReadSlice('\n')
		if err != bufio.ErrBufferFull {
			break
		}
		tooLong = true
	}
	if tooLong {
		line = nil // discard whatever surrounds the line that overflowed the buffer
	}
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return line, tooLong, err
}
