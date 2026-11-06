package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/mcp"
)

// TestMain lets tests re-run this test binary as the gg executable.
func TestMain(m *testing.M) {
	if os.Getenv("GG_TEST_RUN_AS_GG") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestParseMCPFlags(t *testing.T) {
	def := mcp.DefaultLimits()
	cases := []struct {
		name    string
		args    []string
		wantErr string
		check   func(t *testing.T, o mcpOptions)
	}{
		{name: "defaults", check: func(t *testing.T, o mcpOptions) {
			if o.limits.MaxRPS != def.MaxRPS || o.limits.MaxDuration != def.MaxDuration ||
				o.limits.MaxSessions != def.MaxSessions || len(o.limits.AllowHosts) != 0 || o.logLevel != slog.LevelInfo {
				t.Errorf("unexpected defaults: %+v", o)
			}
		}},
		{name: "overrides", args: []string{"--max-rps", "50", "--max-duration", "30s", "--max-sessions", "4", "--log-level", "debug"},
			check: func(t *testing.T, o mcpOptions) {
				if o.limits.MaxRPS != 50 || o.limits.MaxDuration != 30*time.Second || o.limits.MaxSessions != 4 || o.logLevel != slog.LevelDebug {
					t.Errorf("overrides not applied: %+v", o)
				}
			}},
		{name: "allow-host repeatable and comma", args: []string{"--allow-host", "a.example.com", "--allow-host", "b.example.com:8443,localhost"},
			check: func(t *testing.T, o mcpOptions) {
				if got := strings.Join(o.limits.AllowHosts, "|"); got != "a.example.com|b.example.com:8443|localhost" {
					t.Errorf("allow hosts = %q", got)
				}
			}},
		{name: "zero rps", args: []string{"--max-rps", "0"}, wantErr: "max-rps"},
		{name: "negative duration", args: []string{"--max-duration", "-1s"}, wantErr: "max-duration"},
		{name: "zero sessions", args: []string{"--max-sessions", "0"}, wantErr: "max-sessions"},
		{name: "bad log level", args: []string{"--log-level", "loud"}, wantErr: "log-level"},
		{name: "host with scheme", args: []string{"--allow-host", "https://x.example.com"}, wantErr: "allow-host"},
		{name: "positional", args: []string{"extra"}, wantErr: "unexpected argument"},
		{name: "unknown flag", args: []string{"--nope"}, wantErr: "nope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			o, err := parseMCPFlags(c.args, &out)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error()+out.String(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, o)
		})
	}
}

func TestParseMCPFlags_HelpListsEveryFlag(t *testing.T) {
	var out bytes.Buffer
	_, err := parseMCPFlags([]string{"--help"}, &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
	for _, f := range []string{"max-rps", "max-duration", "max-sessions", "allow-host", "log-level"} {
		if !strings.Contains(out.String(), f) {
			t.Errorf("help does not mention --%s:\n%s", f, out.String())
		}
	}
}

func TestClaimStdout_StrayPrintsGoToStderr(t *testing.T) {
	origOut, origErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stdout, os.Stderr = origOut, origErr })

	protocol := claimStdout()
	if protocol != origOut {
		t.Fatal("claimStdout must return the original stdout")
	}
	fmt.Println("stray")
	_ = w.Close()

	got, _ := io.ReadAll(r)
	if string(got) != "stray\n" {
		t.Fatalf("stderr got %q, want the stray print", got)
	}
}

// mcpChild is a `gg mcp` process driven over stdin/stdout.
type mcpChild struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string
	stderr *bytes.Buffer
	all    []string
}

func startMCPChild(t *testing.T, args ...string) *mcpChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"mcp"}, args...)...)
	cmd.Env = append(os.Environ(), "GG_TEST_RUN_AS_GG=1")
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &mcpChild{t: t, cmd: cmd, stdin: in, lines: make(chan string, 64), stderr: stderr}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
		close(c.lines)
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return c
}

func (c *mcpChild) send(frame string) {
	c.t.Helper()
	if _, err := io.WriteString(c.stdin, frame+"\n"); err != nil {
		c.t.Fatalf("write frame: %v", err)
	}
}

// call sends a request and returns the response with the same id.
func (c *mcpChild) call(id int, method, params string) map[string]any {
	c.t.Helper()
	c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params))
	deadline := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-c.lines:
			if !ok {
				c.t.Fatalf("child closed stdout waiting for id %d; stderr:\n%s", id, c.stderr)
			}
			c.all = append(c.all, line)
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				c.t.Fatalf("stdout line is not JSON: %q", line)
			}
			if got, ok := m["id"].(float64); ok && int(got) == id {
				return m
			}
		case <-deadline:
			c.t.Fatalf("timeout waiting for id %d; stderr:\n%s", id, c.stderr)
		}
	}
}

func (c *mcpChild) handshake() {
	c.t.Helper()
	res := c.call(1, "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}`)
	if res["error"] != nil {
		c.t.Fatalf("initialize failed: %v", res["error"])
	}
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
}

func (c *mcpChild) waitExit(d time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		_ = c.cmd.Process.Kill()
		c.t.Fatalf("child did not exit within %s; stderr:\n%s", d, c.stderr)
		return nil
	}
}

// Every byte on stdout must be a JSON-RPC frame, even with debug logging on.
func TestMCPCommand_StdoutIsOnlyJSONRPC(t *testing.T) {
	c := startMCPChild(t, "--log-level", "debug")
	c.handshake()

	list := c.call(2, "resources/list", `{}`)
	if list["error"] != nil {
		t.Fatalf("resources/list: %v", list["error"])
	}
	read := c.call(3, "resources/read", `{"uri":"gg://docs/http-syntax"}`)
	if read["error"] != nil {
		t.Fatalf("resources/read: %v", read["error"])
	}
	c.call(4, "resources/read", `{"uri":"gg://profiles/catalog"}`)
	c.call(5, "prompts/list", `{}`)
	bad := c.call(6, "resources/read", `{"uri":"gg://nope"}`)
	if bad["error"] == nil {
		t.Fatal("unknown resource should return a JSON-RPC error")
	}

	_ = c.stdin.Close()
	if err := c.waitExit(10 * time.Second); err != nil {
		t.Fatalf("exit after stdin EOF: %v\nstderr:\n%s", err, c.stderr)
	}
	for line := range c.lines {
		c.all = append(c.all, line)
	}
	if len(c.all) < 6 {
		t.Fatalf("expected at least 6 frames, got %d", len(c.all))
	}
	for _, line := range c.all {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil || m["jsonrpc"] != "2.0" {
			t.Errorf("stdout line is not a JSON-RPC frame: %q", line)
		}
	}
	if !strings.Contains(c.stderr.String(), "gg mcp ready") {
		t.Errorf("startup log missing from stderr:\n%s", c.stderr)
	}
}

func TestMCPCommand_SIGTERMExitsCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM is not deliverable on windows")
	}
	c := startMCPChild(t)
	c.handshake()
	if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := c.waitExit(10 * time.Second); err != nil {
		t.Fatalf("SIGTERM should exit 0, got %v\nstderr:\n%s", err, c.stderr)
	}
}

func TestMCPCommand_BadFlagExitsNonZeroWithEmptyStdout(t *testing.T) {
	cmd := exec.Command(os.Args[0], "mcp", "--max-rps", "0")
	cmd.Env = append(os.Environ(), "GG_TEST_RUN_AS_GG=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("err = %v, want exit code 2", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout must stay empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "max-rps") {
		t.Errorf("stderr should name the flag: %s", stderr.String())
	}
}
