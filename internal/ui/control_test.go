package ui

import (
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

// TestControlReader_OversizedLine confirms a line over controlLineMaxBytes
// produces exactly one parse_error reply and a clean goroutine exit, per the
// documented (not-yet-resync) behavior in controlReader's own doc comment.
func TestControlReader_OversizedLine(t *testing.T) {
	huge := strings.Repeat("a", controlLineMaxBytes+1000)
	in := strings.NewReader(huge + "\n")
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
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("controlReader did not exit after the oversized line")
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
