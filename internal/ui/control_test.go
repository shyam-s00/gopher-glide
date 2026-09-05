package ui

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// TestParseControlLine is the 2.12 parse table: every documented outcome in
// §2.3, exercised directly against parseControlLine.
func TestParseControlLine(t *testing.T) {
	tests := []struct {
		name          string
		line          string
		wantCmd       string // "" when an error is expected
		wantAmount    int
		wantLabel     string
		wantErrReason string
		wantErrID     string // expected controlError.ID (empty unless noted)
		wantErrCmd    string // expected controlError.Command
	}{
		{name: "valid bias positive", line: `{"id":"c1","command":"bias","amount":5}`, wantCmd: "bias", wantAmount: 5},
		{name: "valid bias negative", line: `{"id":"c1","command":"bias","amount":-3}`, wantCmd: "bias", wantAmount: -3},
		{name: "valid mark", line: `{"id":"c1","command":"mark","label":"deploy-v2-canary"}`, wantCmd: "mark", wantLabel: "deploy-v2-canary"},
		{name: "valid mark no id", line: `{"command":"mark","label":"x"}`, wantCmd: "mark", wantLabel: "x"},
		{name: "valid stop", line: `{"id":"c1","command":"stop"}`, wantCmd: "stop"},
		{name: "extra fields ignored", line: `{"id":"c1","command":"stop","amount":5,"label":"x"}`, wantCmd: "stop"},

		{
			name: "malformed json", line: `not even json`,
			wantErrReason: "parse_error", wantErrID: "", wantErrCmd: "",
		},
		{
			name: "id wrong type", line: `{"id":5,"command":"bias","amount":5}`,
			wantErrReason: "parse_error", wantErrID: "", wantErrCmd: "",
		},
		{
			name: "unknown command", line: `{"id":"c6","command":"pause"}`,
			wantErrReason: "unknown_command", wantErrID: "c6", wantErrCmd: "pause",
		},
		{
			name: "missing command", line: `{"id":"c1"}`,
			wantErrReason: "unknown_command", wantErrID: "c1", wantErrCmd: "",
		},
		{
			name: "bias amount zero", line: `{"id":"c5","command":"bias","amount":0}`,
			wantErrReason: "invalid_argument", wantErrID: "c5", wantErrCmd: "bias",
		},
		{
			name: "bias amount missing", line: `{"id":"c1","command":"bias"}`,
			wantErrReason: "invalid_argument", wantErrID: "c1", wantErrCmd: "bias",
		},
		{
			name: "bias amount non-integer string", line: `{"id":"c1","command":"bias","amount":"five"}`,
			wantErrReason: "invalid_argument", wantErrID: "c1", wantErrCmd: "bias",
		},
		{
			name: "bias amount non-integer float", line: `{"id":"c1","command":"bias","amount":5.5}`,
			wantErrReason: "invalid_argument", wantErrID: "c1", wantErrCmd: "bias",
		},
		{
			name: "mark label empty", line: `{"id":"c7","command":"mark","label":""}`,
			wantErrReason: "invalid_argument", wantErrID: "c7", wantErrCmd: "mark",
		},
		{
			name: "mark label missing", line: `{"id":"c1","command":"mark"}`,
			wantErrReason: "invalid_argument", wantErrID: "c1", wantErrCmd: "mark",
		},
		{
			name: "mark label non-string", line: `{"id":"c1","command":"mark","label":5}`,
			wantErrReason: "invalid_argument", wantErrID: "c1", wantErrCmd: "mark",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, cerr := parseControlLine([]byte(tt.line))

			if tt.wantErrReason == "" {
				if cerr != nil {
					t.Fatalf("unexpected error: %+v", cerr)
				}
				if cmd.Command != tt.wantCmd {
					t.Errorf("Command = %q, want %q", cmd.Command, tt.wantCmd)
				}
				if cmd.Amount != tt.wantAmount {
					t.Errorf("Amount = %d, want %d", cmd.Amount, tt.wantAmount)
				}
				if cmd.Label != tt.wantLabel {
					t.Errorf("Label = %q, want %q", cmd.Label, tt.wantLabel)
				}
				return
			}

			if cerr == nil {
				t.Fatalf("expected error %q, got success: %+v", tt.wantErrReason, cmd)
			}
			if cerr.Reason != tt.wantErrReason {
				t.Errorf("Reason = %q, want %q", cerr.Reason, tt.wantErrReason)
			}
			if cerr.ID != tt.wantErrID {
				t.Errorf("ID = %q, want %q", cerr.ID, tt.wantErrID)
			}
			if cerr.Command != tt.wantErrCmd {
				t.Errorf("Command = %q, want %q", cerr.Command, tt.wantErrCmd)
			}
			if cerr.Message == "" {
				t.Error("Message should not be empty")
			}
		})
	}
}

// TestControlReader_OrderedDelivery confirms controlReader delivers parsed
// commands and parse errors to cmdCh in file order, and exits quietly on EOF.
func TestControlReader_OrderedDelivery(t *testing.T) {
	in := strings.NewReader("{\"id\":\"c1\",\"command\":\"bias\",\"amount\":5}\nnot json\n{\"id\":\"c3\",\"command\":\"stop\"}\n")
	cmdCh := make(chan controlCommand, controlCmdChanCap)
	done := make(chan struct{})
	go func() {
		controlReader(in, cmdCh)
		close(done)
	}()

	var got []controlCommand
	for len(got) < 3 {
		select {
		case c := <-cmdCh:
			got = append(got, c)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d commands", len(got))
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("controlReader did not exit on EOF")
	}

	if got[0].Command != "bias" || got[0].Amount != 5 || got[0].Err != nil {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Err == nil || got[1].Err.Reason != "parse_error" {
		t.Errorf("got[1] = %+v", got[1])
	}
	if got[2].Command != "stop" || got[2].Err != nil {
		t.Errorf("got[2] = %+v", got[2])
	}
}

// TestControlReader_OversizedLine confirms an over-limit line produces one
// parse_error and — the 3.6 fix — resyncs instead of going deaf: a valid
// command right after it must still be processed.
func TestControlReader_OversizedLine(t *testing.T) {
	huge := strings.Repeat("a", controlLineMaxBytes+1000)
	in := strings.NewReader(huge + "\n" + `{"id":"c1","command":"stop"}` + "\n")
	cmdCh := make(chan controlCommand, controlCmdChanCap)
	done := make(chan struct{})
	go func() {
		controlReader(in, cmdCh)
		close(done)
	}()

	select {
	case c := <-cmdCh:
		if c.Err == nil || c.Err.Reason != "parse_error" {
			t.Errorf("want parse_error, got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
	select {
	case c := <-cmdCh:
		if c.Err != nil || c.Command != "stop" {
			t.Errorf("resync failed: want the stop command after the oversized line, got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the post-oversized-line command")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("controlReader did not exit on EOF")
	}
}

// TestControlReader_GarbageJSON_SurvivesAndResyncs is the 3.6 "garbage JSON"
// scenario: malformed lines interleaved with valid commands each get their
// own reply, and never swallow a valid command sitting next to one.
func TestControlReader_GarbageJSON_SurvivesAndResyncs(t *testing.T) {
	in := strings.NewReader(
		"{not: valid, json ]]]\n" +
			"random text, no braces at all\n" +
			`{"id":"c1","command":"bias","amount":5}` + "\n" +
			"%%%% garbled #### \x00\x01\x02\n" +
			`{"id":"c2","command":"stop"}` + "\n",
	)
	cmdCh := make(chan controlCommand, controlCmdChanCap)
	done := make(chan struct{})
	go func() {
		controlReader(in, cmdCh)
		close(done)
	}()

	gotErrs, gotCmds := 0, 0
	for range 5 { // 3 garbage lines + 2 valid commands
		select {
		case c := <-cmdCh:
			if c.Err != nil {
				if c.Err.Reason != "parse_error" {
					t.Errorf("garbage line got reason %q, want parse_error", c.Err.Reason)
				}
				gotErrs++
			} else {
				gotCmds++
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d errors, %d commands", gotErrs, gotCmds)
		}
	}
	if gotErrs != 3 || gotCmds != 2 {
		t.Errorf("got %d parse_errors and %d commands, want 3 and 2", gotErrs, gotCmds)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("controlReader did not exit on EOF")
	}
}

// TestControlReader_BinaryGarbage_100KiB is the 3.6 "pipe /dev/urandom"
// scenario, made deterministic (fixed seed): ~100 KiB of binary garbage
// must not crash the reader, and a trailing valid command must get through.
func TestControlReader_BinaryGarbage_100KiB(t *testing.T) {
	var buf bytes.Buffer
	rng := rand.New(rand.NewSource(42))
	for buf.Len() < 100_000 {
		n := 50 + rng.Intn(400) // vary line length so this doesn't parallel controlLineMaxBytes
		line := make([]byte, n)
		_, _ = rng.Read(line)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	buf.WriteString(`{"id":"cLast","command":"stop"}` + "\n")

	cmdCh := make(chan controlCommand, controlCmdChanCap)
	done := make(chan struct{})
	go func() {
		controlReader(bytes.NewReader(buf.Bytes()), cmdCh)
		close(done)
	}()

	var sawStop bool
	timeout := time.After(5 * time.Second)
drain:
	for {
		select {
		case c := <-cmdCh:
			if c.Err == nil && c.Command == "stop" {
				sawStop = true
				break drain
			}
			if c.Err != nil && c.Err.Reason != "parse_error" {
				t.Errorf("unexpected error reason %q for garbage line", c.Err.Reason)
			}
		case <-timeout:
			t.Fatal("timed out draining binary-garbage stream")
		}
	}
	if !sawStop {
		t.Fatal("trailing stop command after ~100KiB of binary garbage was never delivered")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("controlReader did not exit on EOF")
	}
}

// TestControlReader_EmptyInput_ExitsQuietly confirms immediate EOF (e.g.
// --control stdin against a closed/empty pipe) produces zero replies.
func TestControlReader_EmptyInput_ExitsQuietly(t *testing.T) {
	cmdCh := make(chan controlCommand, controlCmdChanCap)
	done := make(chan struct{})
	go func() {
		controlReader(strings.NewReader(""), cmdCh)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("controlReader did not exit on empty input")
	}
	select {
	case c := <-cmdCh:
		t.Errorf("expected no commands, got %+v", c)
	default:
	}
}

// TestControlReader_CRLF is the 3.2 Windows line-ending check: a cmd.exe or
// PowerShell pipe writes \r\n, not \n. readControlLine trims the trailing
// \r, so no stray \r should ever leak into a parsed field.
func TestControlReader_CRLF(t *testing.T) {
	in := strings.NewReader("{\"id\":\"c1\",\"command\":\"bias\",\"amount\":5}\r\n{\"id\":\"c2\",\"command\":\"stop\"}\r\n")
	cmdCh := make(chan controlCommand, controlCmdChanCap)
	done := make(chan struct{})
	go func() {
		controlReader(in, cmdCh)
		close(done)
	}()

	var got []controlCommand
	for len(got) < 2 {
		select {
		case c := <-cmdCh:
			got = append(got, c)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d commands", len(got))
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("controlReader did not exit on EOF")
	}

	if got[0].Command != "bias" || got[0].Amount != 5 || got[0].Err != nil {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Command != "stop" || got[1].Err != nil {
		t.Errorf("got[1] = %+v", got[1])
	}
}

// TestControlReader_NoTrailingNewline confirms a final command with no
// trailing newline — the stream just closes right after, as happens when a
// Windows console pipe is torn down mid-line — is still parsed, not dropped.
func TestControlReader_NoTrailingNewline(t *testing.T) {
	in := strings.NewReader(`{"id":"c1","command":"mark","label":"eof-close"}`)
	cmdCh := make(chan controlCommand, controlCmdChanCap)
	done := make(chan struct{})
	go func() {
		controlReader(in, cmdCh)
		close(done)
	}()

	select {
	case c := <-cmdCh:
		if c.Command != "mark" || c.Label != "eof-close" || c.Err != nil {
			t.Errorf("got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("controlReader did not exit on EOF")
	}
}
