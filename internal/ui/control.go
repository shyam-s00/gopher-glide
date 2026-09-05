package ui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// controlLineMaxBytes caps a single control command line — a designed
// protocol limit (§3.2), not an incidental one.
const controlLineMaxBytes = 64 * 1024

// controlCommand is one parsed, validated command from the stdin control
// protocol (LCP), or a failed one carrying the error to reply with instead.
// See ignore/live-control-protocol.md §2.2.
type controlCommand struct {
	ID      string // caller-chosen correlation id; empty if omitted
	Command string // "bias" | "mark" | "stop"
	Amount  int    // bias only: signed, non-zero delta
	Label   string // mark only: non-empty annotation text

	// Err is set when the line failed to parse or validate. Every other
	// field is zero; the select loop should reply with Err, not execute
	// anything (§2.3).
	Err *controlError
}

// controlError is a typed command-line failure, carrying the §2.3 error
// reason plus whatever of id/command survived parsing.
type controlError struct {
	Reason  string // parse_error | unknown_command | invalid_argument
	ID      string // set for unknown_command / invalid_argument only
	Command string // set for unknown_command / invalid_argument only
	Message string
}

func (e *controlError) Error() string { return e.Message }

// rawControlCommand keeps Amount/Label raw so a bad type there doesn't fail
// decoding — only a non-string id/command, or broken JSON, is parse_error.
// Amount/Label are validated separately, into invalid_argument (§2.3).
type rawControlCommand struct {
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Amount  json.RawMessage `json:"amount"`
	Label   json.RawMessage `json:"label"`
}

// parseControlLine decodes and validates one NDJSON command line, returning
// either a ready-to-execute controlCommand or a *controlError typed per §2.3.
func parseControlLine(line []byte) (controlCommand, *controlError) {
	var raw rawControlCommand
	if err := json.Unmarshal(line, &raw); err != nil {
		// id/command are non-string or the line isn't JSON at all — neither
		// is recoverable, so the reply carries no id/command (§2.3).
		return controlCommand{}, &controlError{
			Reason:  "parse_error",
			Message: fmt.Sprintf("invalid command line: %v", err),
		}
	}

	switch raw.Command {
	case "bias":
		amount, ok := decodeInt(raw.Amount)
		if !ok || amount == 0 {
			return controlCommand{}, &controlError{
				Reason: "invalid_argument", ID: raw.ID, Command: raw.Command,
				Message: "amount must be a non-zero integer",
			}
		}
		return controlCommand{ID: raw.ID, Command: raw.Command, Amount: amount}, nil

	case "mark":
		label, ok := decodeString(raw.Label)
		if !ok || label == "" {
			return controlCommand{}, &controlError{
				Reason: "invalid_argument", ID: raw.ID, Command: raw.Command,
				Message: "label must be a non-empty string",
			}
		}
		return controlCommand{ID: raw.ID, Command: raw.Command, Label: label}, nil

	case "stop":
		return controlCommand{ID: raw.ID, Command: raw.Command}, nil

	default:
		return controlCommand{}, &controlError{
			Reason: "unknown_command", ID: raw.ID, Command: raw.Command,
			Message: fmt.Sprintf("unknown command %q", raw.Command),
		}
	}
}

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

// controlReader reads r line by line into cmdCh — parsing only, not
// executing (§3.2). Sends block rather than drop on a full channel (every
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
