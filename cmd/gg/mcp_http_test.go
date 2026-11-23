package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const httpTestToken = "test-token-0123456789"

func TestParseMCPFlags_TokenFromEnvironment(t *testing.T) {
	t.Setenv(tokenEnv, httpTestToken)
	var out bytes.Buffer
	o, err := parseMCPFlags([]string{"--port", "0", "--bind", "0.0.0.0"}, &out)
	if err != nil || o.token != httpTestToken {
		t.Fatalf("token from env: %+v %v", o, err)
	}
	o, err = parseMCPFlags([]string{"--port", "0", "--auth-token", "flag-token-0123456789"}, &out)
	if err != nil || o.token != "flag-token-0123456789" {
		t.Fatalf("the flag must win over the environment: %+v %v", o, err)
	}
	if _, err = parseMCPFlags([]string{"--max-rps", "5"}, &out); err != nil {
		t.Fatalf("a token in the environment must not matter for stdio: %v", err)
	}
}

// httpChild is a `gg mcp --port` process.
type httpChild struct {
	cmd *exec.Cmd
	url string

	mu     sync.Mutex
	stderr bytes.Buffer
	done   chan error
}

func (c *httpChild) logs() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stderr.String()
}

var listenURL = regexp.MustCompile(`url=(http://\S+)`)

func startHTTPChild(t *testing.T, env []string, args ...string) *httpChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"mcp", "--port", "0"}, args...)...)
	cmd.Env = append(append(os.Environ(), "GG_TEST_RUN_AS_GG=1"), env...)
	pipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &httpChild{cmd: cmd, done: make(chan error, 1)}
	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			c.mu.Lock()
			c.stderr.WriteString(sc.Text() + "\n")
			c.mu.Unlock()
			if m := listenURL.FindStringSubmatch(sc.Text()); m != nil {
				select {
				case ready <- m[1]:
				default:
				}
			}
		}
		c.done <- cmd.Wait()
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	select {
	case c.url = <-ready:
	case err := <-c.done:
		t.Fatalf("gg mcp exited early: %v\n%s", err, c.logs())
	case <-time.After(15 * time.Second):
		t.Fatalf("gg mcp never reported its address:\n%s", c.logs())
	}
	return c
}

func (c *httpChild) client(t *testing.T, token string) (*sdk.ClientSession, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if token != "" {
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer "+token)
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).
		Connect(ctx, &sdk.StreamableClientTransport{Endpoint: c.url, HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (map[string]any, bool, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*sdk.TextContent); ok {
			text = tc.Text
		}
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out, res.IsError, text
}

// Edge 31 on the real binary: no token, no public listener.
func TestMCPHTTP_RefusesPublicBindWithoutToken(t *testing.T) {
	cmd := exec.Command(os.Args[0], "mcp", "--port", "0", "--bind", "0.0.0.0")
	cmd.Env = append(os.Environ(), "GG_TEST_RUN_AS_GG=1", tokenEnv+"=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 || !strings.Contains(stderr.String(), "without authentication") {
		t.Fatalf("exit = %v, stderr:\n%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "ready") {
		t.Error("it must refuse before it listens")
	}
}

func TestMCPHTTP_ServesToolsBehindTheToken(t *testing.T) {
	c := startHTTPChild(t, nil, "--auth-token", httpTestToken)
	if !strings.HasPrefix(c.url, "http://127.0.0.1:") || !strings.HasSuffix(c.url, "/mcp") {
		t.Fatalf("url = %q", c.url)
	}
	if strings.Contains(c.logs(), httpTestToken) {
		t.Error("the token must never be logged")
	}

	if _, err := c.client(t, ""); err == nil {
		t.Error("a client without the token must be refused")
	}
	cs, err := c.client(t, httpTestToken)
	if err != nil {
		t.Fatalf("with the token: %v\n%s", err, c.logs())
	}
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil || len(list.Tools) != 11 {
		t.Fatalf("tools = %v, err = %v", len(list.Tools), err)
	}
	out, isErr, text := callTool(t, cs, "validate_http_file", map[string]any{"content": "GET http://127.0.0.1:9/x\n"})
	if isErr || out["ok"] != true {
		t.Fatalf("validate: %v %s", out, text)
	}

	req, _ := http.NewRequest(http.MethodPost, c.url, strings.NewReader("{}"))
	req.Host = "rebind.evil.com"
	req.Header.Set("Authorization", "Bearer "+httpTestToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("rebound Host got %d", resp.StatusCode)
	}
}

// SIGTERM with a live simulation: the server exits, the child stops by itself
// and its snapshot is kept.
func TestMCPHTTP_SIGTERMStopsRunningSimulation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGTERM on Windows")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	srv, _ := targetServer(t)
	snapDir := t.TempDir()
	c := startHTTPChild(t, []string{"GG_SNAP_DIR=" + snapDir}, "--auth-token", httpTestToken)
	cs, err := c.client(t, httpTestToken)
	if err != nil {
		t.Fatal(err)
	}
	_, isErr, text := callTool(t, cs, "start_simulation", map[string]any{
		"http_content": "GET " + srv.URL + "/ok\n", "profile": "load", "peak_rps": 10, "duration": "60s", "snap": true,
	})
	if isErr {
		t.Fatalf("start: %s", text)
	}
	pid := childPID(t, c.cmd.Process.Pid)
	time.Sleep(1500 * time.Millisecond) // let it record something

	begin := time.Now()
	if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatalf("exit after SIGTERM: %v\n%s", err, c.logs())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("no exit after SIGTERM:\n%s", c.logs())
	}
	t.Logf("exited %v after SIGTERM", time.Since(begin).Round(time.Millisecond))

	if processAlive(pid) {
		t.Errorf("simulation process %d outlived the server", pid)
	}
	snaps, _ := filepath.Glob(filepath.Join(snapDir, "*.snap"))
	if len(snaps) != 1 {
		t.Errorf("the snapshot should have been finalized, found %v", snaps)
	}
}
