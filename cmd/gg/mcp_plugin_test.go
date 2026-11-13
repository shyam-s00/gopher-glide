package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shyam-s00/gopher-glide/internal/mcp"
)

func TestParseMCPFlags_PluginDaemonAndAttach(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
		check   func(t *testing.T, o mcpOptions)
	}{
		{name: "user slot default", check: func(t *testing.T, o mcpOptions) {
			if o.limits.MaxUserSessions != 1 {
				t.Errorf("max user sessions = %d", o.limits.MaxUserSessions)
			}
		}},
		{name: "user slot override", args: []string{"--max-user-sessions", "3"}, check: func(t *testing.T, o mcpOptions) {
			if o.limits.MaxUserSessions != 3 {
				t.Errorf("max user sessions = %d", o.limits.MaxUserSessions)
			}
		}},
		{name: "negative user slots", args: []string{"--max-user-sessions", "-1"}, wantErr: "max-user-sessions"},
		{name: "exit on eof with port", args: []string{"--port", "0", "--exit-on-stdin-eof"}, check: func(t *testing.T, o mcpOptions) {
			if !o.exitOnEOF || !o.http {
				t.Errorf("%+v", o)
			}
		}},
		{name: "exit on eof needs port", args: []string{"--exit-on-stdin-eof"}, wantErr: "--port"},
		{name: "attach without url", args: []string{"--attach"}, check: func(t *testing.T, o mcpOptions) {
			if !o.attach || o.attachURL != "" || o.http {
				t.Errorf("%+v", o)
			}
		}},
		{name: "attach with url", args: []string{"--attach", "http://127.0.0.1:5000/mcp"}, check: func(t *testing.T, o mcpOptions) {
			if !o.attach || o.attachURL != "http://127.0.0.1:5000/mcp" {
				t.Errorf("%+v", o)
			}
		}},
		{name: "attach with a token", args: []string{"--attach", "--auth-token", "token-0123456789abcdef"}, check: func(t *testing.T, o mcpOptions) {
			if o.token != "token-0123456789abcdef" {
				t.Errorf("%+v", o)
			}
		}},
		{name: "attach url must be http", args: []string{"--attach", "ftp://x/mcp"}, wantErr: "http"},
		{name: "attach url garbage", args: []string{"--attach", "nonsense"}, wantErr: "http"},
		{name: "attach with port", args: []string{"--attach", "--port", "0"}, wantErr: "cannot be combined"},
		{name: "attach with bind", args: []string{"--attach", "--bind", "127.0.0.1"}, wantErr: "cannot be combined"},
		{name: "attach with exit on eof", args: []string{"--attach", "--exit-on-stdin-eof"}, wantErr: "cannot be combined"},
		{name: "positional without attach", args: []string{"http://x/mcp"}, wantErr: "unexpected argument"},
		{name: "two positionals", args: []string{"--attach", "http://a/mcp", "http://b/mcp"}, wantErr: "unexpected argument"},
		{name: "token without port or attach", args: []string{"--auth-token", "token-0123456789abcdef"}, wantErr: "--auth-token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			o, err := parseMCPFlags(c.args, &out)
			switch {
			case c.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
			case err != nil:
				t.Fatal(err)
			case c.check != nil:
				c.check(t, o)
			}
		})
	}
}

func TestConnectURL(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:5000": "http://127.0.0.1:5000/mcp",
		"[::1]:5000":     "http://[::1]:5000/mcp",
		"0.0.0.0:5000":   "http://127.0.0.1:5000/mcp",
		"[::]:5000":      "http://127.0.0.1:5000/mcp",
	} {
		addr, err := netResolve(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := connectURL(addr); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}

// daemon is a `gg mcp --port 0 --exit-on-stdin-eof` process, as an IDE would run it.
type daemon struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	url    string
	base   string
	pid    int
	exited chan error

	mu     sync.Mutex
	stderr bytes.Buffer
}

func (d *daemon) logs() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stderr.String()
}

// isolateCache points the user cache dir, and so the discovery files, at a temp dir.
func isolateCache(t *testing.T) {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(cache, "xdg"))
	t.Setenv("LocalAppData", cache)
}

func startDaemon(t *testing.T, dir string, env []string, args ...string) *daemon {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"mcp", "--port", "0", "--exit-on-stdin-eof"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GG_TEST_RUN_AS_GG=1"), env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d := &daemon{cmd: cmd, stdin: stdin, exited: make(chan error, 1)}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			d.mu.Lock()
			d.stderr.WriteString(sc.Text() + "\n")
			d.mu.Unlock()
		}
	}()

	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
		d.exited <- cmd.Wait()
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	select {
	case line := <-lines:
		var ev struct {
			Event string `json:"event"`
			URL   string `json:"url"`
			PID   int    `json:"pid"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Event != "listening" || ev.URL == "" {
			t.Fatalf("first stdout line = %q (%v); want the listening event", line, err)
		}
		d.url, d.pid = ev.URL, ev.PID
		d.base = strings.TrimSuffix(ev.URL, mcp.EndpointPath)
	case <-time.After(15 * time.Second):
		t.Fatalf("the daemon never announced itself:\n%s", d.logs())
	}
	go func() {
		for range lines {
		}
	}()
	return d
}

func (d *daemon) waitExit(t *testing.T, within time.Duration) error {
	t.Helper()
	select {
	case err := <-d.exited:
		d.exited <- err
		return err
	case <-time.After(within):
		t.Fatalf("daemon still running after %s:\n%s", within, d.logs())
		return nil
	}
}

func (d *daemon) client(t *testing.T, token string) *sdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+token)
		return http.DefaultTransport.RoundTrip(r)
	})}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).
		Connect(ctx, &sdk.StreamableClientTransport{Endpoint: d.url, HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func postPlugin(t *testing.T, d *daemon, path, token string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, d.base+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

const daemonToken = "daemon-token-0123456789"

func TestDaemon_AnnouncesItselfRegistersAndLeavesOnStdinEOF(t *testing.T) {
	isolateCache(t)
	ws := t.TempDir()
	d := startDaemon(t, ws, []string{"GG_MCP_TOKEN=" + daemonToken})

	if d.pid != d.cmd.Process.Pid {
		t.Errorf("announced pid %d, process is %d", d.pid, d.cmd.Process.Pid)
	}
	rec, ok := mcp.FindDaemon(ws)
	if !ok || rec.URL != d.url || rec.Token != daemonToken || rec.PID != d.pid {
		t.Fatalf("discovery record = %+v, %v", rec, ok)
	}
	resp, err := http.Post(d.base+mcp.PluginControlPath, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the token passed in the environment must guard /plugin/*: %d", resp.StatusCode)
	}
	if strings.Contains(d.logs(), daemonToken) {
		t.Errorf("the token leaked into the logs:\n%s", d.logs())
	}

	_ = d.stdin.Close()
	if err := d.waitExit(t, 15*time.Second); err != nil {
		t.Errorf("a daemon whose stdin closed should exit cleanly: %v\n%s", err, d.logs())
	}
	if _, ok := mcp.FindDaemon(ws); ok {
		t.Error("the discovery record must be removed on exit")
	}
}

func startUserRun(t *testing.T, d *daemon, target, snapDir string) string {
	t.Helper()
	status, out := postPlugin(t, d, mcp.PluginStartPath, daemonToken, map[string]any{
		"http_content": "GET " + target + "/ok\n", "profile": "load", "peak_rps": 10, "duration": "60s", "snap": true,
	})
	if status != 200 {
		t.Fatalf("/plugin/start: %d %v\n%s", status, out, d.logs())
	}
	return out["session_id"].(string)
}

// A closed stdin is how an IDE crash looks to the daemon: runs stop normally,
// keep their snapshots, and no gg process is left behind.
func TestDaemon_StdinEOFStopsRunsAndLeavesNoProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses pgrep")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	isolateCache(t)
	target, _ := targetServer(t)
	snapDir := t.TempDir()
	d := startDaemon(t, t.TempDir(), []string{"GG_MCP_TOKEN=" + daemonToken, "GG_SNAP_DIR=" + snapDir})
	startUserRun(t, d, target.URL, snapDir)
	run := childPID(t, d.cmd.Process.Pid)

	_ = d.stdin.Close()
	d.waitExit(t, 30*time.Second)

	waitDead(t, run, "the run")
	snaps, _ := filepath.Glob(filepath.Join(snapDir, "*.snap"))
	if len(snaps) != 1 {
		t.Errorf("the run should have been stopped normally and kept its snapshot, found %v", snaps)
	}
}

func TestDaemon_KillingItLeavesNoOrphanedRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses pgrep")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	isolateCache(t)
	target, _ := targetServer(t)
	snapDir := t.TempDir()
	d := startDaemon(t, t.TempDir(), []string{"GG_MCP_TOKEN=" + daemonToken, "GG_SNAP_DIR=" + snapDir})
	startUserRun(t, d, target.URL, snapDir)
	run := childPID(t, d.cmd.Process.Pid)

	if err := d.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitDead(t, run, "the run of a killed daemon")
}

func waitDead(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("%s (pid %d) is still running", what, pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A stdio client attached to the plugin's daemon runs its simulation there.
func TestAttach_StdioClientSharesTheDaemonsSessions(t *testing.T) {
	isolateCache(t)
	ws := t.TempDir()
	target, _ := targetServer(t)
	d := startDaemon(t, ws, []string{"GG_MCP_TOKEN=" + daemonToken})

	c := startMCPChildIn(t, ws, nil, "--attach")
	c.handshake()
	out, isErr, text := c.tool(10, "start_simulation", map[string]any{
		"http_content": "GET " + target.URL + "/ok\n", "profile": "load", "peak_rps": 10, "duration": "60s",
	})
	if isErr {
		t.Fatalf("start through the bridge: %s\n%s", text, c.stderr)
	}
	id := out["session_id"].(string)

	cs := d.client(t, daemonToken)
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "get_live_telemetry", Arguments: map[string]any{"session_id": id}})
	if err != nil || res.IsError {
		t.Fatalf("the daemon does not know the bridge's run: %v %+v", err, res)
	}

	_, isErr, text = c.tool(11, "stop_simulation", map[string]any{})
	if isErr {
		t.Fatalf("stop through the bridge: %s", text)
	}
	_ = c.stdin.Close()
	if err := c.waitExit(10 * time.Second); err != nil {
		t.Errorf("attach should exit when its client goes: %v\n%s", err, c.stderr)
	}
}

func TestAttach_WithoutADaemonServesInProcess(t *testing.T) {
	isolateCache(t)
	c := startMCPChildIn(t, t.TempDir(), nil, "--attach")
	c.handshake()
	res := c.call(2, "tools/list", `{}`)
	tools := res["result"].(map[string]any)["tools"].([]any)
	if len(tools) < 11 {
		t.Fatalf("tools = %d", len(tools))
	}
	_ = c.stdin.Close()
	_ = c.waitExit(10 * time.Second)
	if !strings.Contains(c.stderr.String(), "serving in-process") {
		t.Errorf("stderr should say why:\n%s", c.stderr)
	}
}

func TestAttach_StaleRecordIsIgnored(t *testing.T) {
	isolateCache(t)
	ws := t.TempDir()
	dead := exec.Command(os.Args[0], "-test.run=^$")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := mcp.RegisterDaemon(ws, mcp.Daemon{URL: "http://127.0.0.1:1/mcp", PID: dead.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	c := startMCPChildIn(t, ws, nil, "--attach")
	c.handshake()
	if res := c.call(2, "tools/list", `{}`); res["error"] != nil {
		t.Fatalf("tools/list: %v", res["error"])
	}
	_ = c.stdin.Close()
	_ = c.waitExit(10 * time.Second)
}

func TestAttach_ARecordForAProcessThatDoesNotListenIsIgnored(t *testing.T) {
	isolateCache(t)
	ws := t.TempDir()
	// This test process is alive but nothing listens on port 1.
	if _, err := mcp.RegisterDaemon(ws, mcp.Daemon{URL: "http://127.0.0.1:1/mcp", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	c := startMCPChildIn(t, ws, nil, "--attach")
	c.handshake()
	if res := c.call(2, "tools/list", `{}`); res["error"] != nil {
		t.Fatalf("tools/list: %v", res["error"])
	}
	_ = c.stdin.Close()
	_ = c.waitExit(10 * time.Second)
}

func TestAttach_ExplicitUnreachableURLFails(t *testing.T) {
	isolateCache(t)
	c := startMCPChildIn(t, t.TempDir(), nil, "--attach", "http://127.0.0.1:1/mcp")
	err := c.waitExit(10 * time.Second)
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 || !strings.Contains(c.stderr.String(), "cannot reach") {
		t.Fatalf("exit = %v\n%s", err, c.stderr)
	}
}

func netResolve(hostport string) (net.Addr, error) {
	return net.ResolveTCPAddr("tcp", hostport)
}
