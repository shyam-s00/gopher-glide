package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shyam-s00/gopher-glide/internal/agent"
)

func TestParseAutopilotFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
		check   func(t *testing.T, c autopilotCmd)
	}{
		{name: "generate with text", args: []string{"generate", "an", "items", "API"}, check: func(t *testing.T, c autopilotCmd) {
			if c.mode != agent.ModeGenerate || c.task != "an items API" || c.out != "requests.http" || c.maxTurns != agent.DefaultMaxTurns {
				t.Errorf("%+v", c)
			}
		}},
		{name: "flags after the task", args: []string{"generate", "my api", "--out", "x.http", "--max-turns", "2", "--model", "qwen", "--base-url", "http://localhost:11434/v1"}, check: func(t *testing.T, c autopilotCmd) {
			if c.task != "my api" || c.out != "x.http" || c.maxTurns != 2 || c.model.Name != "qwen" || c.model.BaseURL == "" {
				t.Errorf("%+v", c)
			}
		}},
		{name: "run", args: []string{"run", "find", "the", "ceiling", "--max-rps", "200", "--allow-host", "localhost:8080"}, check: func(t *testing.T, c autopilotCmd) {
			if c.mode != agent.ModeRun || c.limits.MaxRPS != 200 || len(c.limits.AllowHosts) != 1 {
				t.Errorf("%+v", c)
			}
		}},
		{name: "triage defaults its question", args: []string{"triage"}, check: func(t *testing.T, c autopilotCmd) {
			if c.mode != agent.ModeTriage || c.task == "" {
				t.Errorf("%+v", c)
			}
		}},
		{name: "acp", args: []string{"--acp"}, check: func(t *testing.T, c autopilotCmd) {
			if !c.acp || c.mode != "" {
				t.Errorf("%+v", c)
			}
		}},
		{name: "acp with url", args: []string{"--acp", "--mcp-url", "http://127.0.0.1:9/mcp", "--auth-token", "t"}, check: func(t *testing.T, c autopilotCmd) {
			if c.mcpURL != "http://127.0.0.1:9/mcp" || c.token != "t" {
				t.Errorf("%+v", c)
			}
		}},
		{name: "no mode", args: nil, wantErr: "give a mode"},
		{name: "unknown mode", args: []string{"fly"}, wantErr: `unknown mode "fly"`},
		{name: "generate needs a task", args: []string{"generate"}, wantErr: "needs a task"},
		{name: "acp with a mode", args: []string{"run", "x", "--acp"}, wantErr: "cannot be combined"},
		{name: "acp with arguments", args: []string{"--acp", "stray"}, wantErr: "takes no arguments"},
		{name: "bad log level", args: []string{"run", "x", "--log-level", "loud"}, wantErr: "invalid --log-level"},
		{name: "zero turns", args: []string{"generate", "x", "--max-turns", "0"}, wantErr: "at least 1"},
		{name: "bad limits", args: []string{"run", "x", "--max-rps", "0"}, wantErr: "max"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(mcpURLEnv, "")
			t.Setenv(tokenEnv, "")
			got, err := parseAutopilotFlags(c.args, io.Discard)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, got)
		})
	}
}

func TestParseAutopilotFlags_EnvSuppliesTheDaemon(t *testing.T) {
	t.Setenv(mcpURLEnv, "http://127.0.0.1:7/mcp")
	t.Setenv(tokenEnv, "secret-token-123456")
	c, err := parseAutopilotFlags([]string{"--acp"}, io.Discard)
	if err != nil || c.mcpURL != "http://127.0.0.1:7/mcp" || c.token != "secret-token-123456" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestWithSpec_InlinesASpecFileOnlyForGenerate(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	_ = os.WriteFile(spec, []byte("openapi: 3.0.0\n"), 0o600)
	got, err := withSpec(spec, agent.ModeGenerate)
	if err != nil || !strings.Contains(got, "openapi: 3.0.0") {
		t.Errorf("generate: %q %v", got, err)
	}
	if got, _ := withSpec(spec, agent.ModeRun); got != spec {
		t.Errorf("run changed the task: %q", got)
	}
	if got, _ := withSpec("a plain description", agent.ModeGenerate); got != "a plain description" {
		t.Errorf("text changed: %q", got)
	}
	big := filepath.Join(dir, "big.json")
	_ = os.WriteFile(big, bytes.Repeat([]byte("x"), maxSpecBytes+1), 0o600)
	if _, err := withSpec(big, agent.ModeGenerate); err == nil {
		t.Error("an oversized spec was inlined")
	}
}

func TestMCPInitCLI(t *testing.T) {
	dir := t.TempDir()
	acp := filepath.Join(t.TempDir(), "jb", "acp.json")
	var out, errOut bytes.Buffer

	code := runMCPInit([]string{"--dir", dir, "--ide", "claude", "--gg-path", "/usr/local/bin/gg", "--register-acp", "--acp-file", acp}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, p := range []string{".mcp.json", ".claude/skills/gg/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s missing", p)
		}
	}
	b, err := os.ReadFile(acp)
	if err != nil || !strings.Contains(string(b), "GopherGlide Autopilot") || !strings.Contains(string(b), "use_custom_mcp") {
		t.Fatalf("acp.json = %s (%v)", b, err)
	}
	if !strings.Contains(out.String(), "restart the IDE") || !strings.Contains(out.String(), "created") {
		t.Errorf("output:\n%s", out.String())
	}

	out.Reset()
	if code := runMCPInit([]string{"--dir", dir, "--ide", "claude", "--gg-path", "/usr/local/bin/gg", "--register-acp", "--acp-file", acp}, &out, &errOut); code != 0 {
		t.Fatalf("second run exit %d: %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "created") || strings.Contains(out.String(), "added") || strings.Count(out.String(), "unchanged") != 3 {
		t.Errorf("second run was not a no-op:\n%s", out.String())
	}
}

func TestMCPInitCLI_PrintTouchesNothingAndAcpNeedsTheFlag(t *testing.T) {
	dir := t.TempDir()
	acp := filepath.Join(t.TempDir(), "acp.json")
	var out, errOut bytes.Buffer
	if code := runMCPInit([]string{"--dir", dir, "--print", "--register-acp", "--acp-file", acp, "--gg-path", "/gg"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("--print wrote %d entries", len(entries))
	}
	if _, err := os.Stat(acp); err == nil {
		t.Error("--print wrote acp.json")
	}
	if !strings.Contains(out.String(), "would-created") || !strings.Contains(out.String(), `"autopilot"`) {
		t.Errorf("output:\n%s", out.String())
	}

	// Without --register-acp, init never goes near the user-global file.
	out.Reset()
	if code := runMCPInit([]string{"--dir", dir, "--gg-path", "/gg"}, &out, &errOut); code != 0 || strings.Contains(out.String(), "acp.json") {
		t.Errorf("exit %d, output:\n%s", code, out.String())
	}
	for _, bad := range [][]string{{"--ide", "emacs"}, {"--acp-file", acp}, {"stray"}} {
		if code := runMCPInit(bad, io.Discard, io.Discard); code != 2 {
			t.Errorf("%v: exit %d, want 2", bad, code)
		}
	}
}

func TestMCPInitCLI_InvalidACPFileIsRefusedAndKept(t *testing.T) {
	acp := filepath.Join(t.TempDir(), "acp.json")
	_ = os.WriteFile(acp, []byte("{nope"), 0o600)
	var out, errOut bytes.Buffer
	code := runMCPInit([]string{"--dir", t.TempDir(), "--ide", "jetbrains", "--gg-path", "/gg", "--register-acp", "--acp-file", acp}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "not valid JSON") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if b, _ := os.ReadFile(acp); string(b) != "{nope" {
		t.Errorf("acp.json changed: %q", b)
	}
}

// acpProc is `gg autopilot --acp` as a JetBrains or Zed client would run it.
type acpProc struct {
	t     *testing.T
	cmd   *exec.Cmd
	in    io.WriteCloser
	lines chan map[string]any
	mu    sync.Mutex
	next  int
}

func startACPProc(t *testing.T, env []string, args ...string) *acpProc {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"autopilot", "--acp"}, args...)...)
	cmd.Env = append(append(os.Environ(), "GG_TEST_RUN_AS_GG=1", "GEMINI_API_KEY=", "GOOGLE_API_KEY=", "GG_AUTOPILOT_BASE_URL=", "GG_SNAP_DIR="+t.TempDir()), env...)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &acpProc{t: t, cmd: cmd, in: in, lines: make(chan map[string]any, 1024)}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("stdout carried a non-protocol line: %q", sc.Text())
				continue
			}
			p.lines <- m
		}
		close(p.lines)
	}()
	t.Cleanup(func() {
		_ = in.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			t.Errorf("gg autopilot --acp did not exit when stdin closed\nstderr:\n%s", stderr.String())
		}
	})
	return p
}

func (p *acpProc) send(method string, params any, notify bool) int {
	p.mu.Lock()
	p.next++
	id := p.next
	p.mu.Unlock()
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if !notify {
		msg["id"] = id
	}
	b, _ := json.Marshal(msg)
	if _, err := p.in.Write(append(b, '\n')); err != nil {
		p.t.Fatal(err)
	}
	return id
}

// until reads messages until f returns true; replies are matched by id.
func (p *acpProc) until(timeout time.Duration, f func(m map[string]any) bool) {
	p.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case m, ok := <-p.lines:
			if !ok {
				p.t.Fatal("gg autopilot --acp closed its output")
			}
			if f(m) {
				return
			}
		case <-deadline:
			p.t.Fatalf("timed out after %s", timeout)
		}
	}
}

func (p *acpProc) request(method string, params any) map[string]any {
	p.t.Helper()
	id := p.send(method, params, false)
	var reply map[string]any
	p.until(30*time.Second, func(m map[string]any) bool {
		if got, ok := m["id"].(float64); ok && int(got) == id && m["method"] == nil {
			reply = m
			return true
		}
		return false
	})
	return reply
}

func (p *acpProc) session(cwd string) string {
	p.t.Helper()
	if r := p.request("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}); r["error"] != nil {
		p.t.Fatalf("initialize: %v", r["error"])
	}
	r := p.request("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	if r["error"] != nil {
		p.t.Fatalf("session/new: %v", r["error"])
	}
	return r["result"].(map[string]any)["sessionId"].(string)
}

func acpPrompt(sid, text string) map[string]any {
	return map[string]any{"sessionId": sid, "prompt": []any{map[string]any{"type": "text", "text": text}}}
}

func updateOf(m map[string]any) (kind string, u map[string]any) {
	if m["method"] != "session/update" {
		return "", nil
	}
	u = m["params"].(map[string]any)["update"].(map[string]any)
	return u["sessionUpdate"].(string), u
}

func TestAutopilotACP_E2E_FindCeilingSlashCommandWithoutAnyKey(t *testing.T) {
	if testing.Short() {
		t.Skip("runs for about 25 seconds")
	}
	srv := newKneeServer(t, 1_000_000)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "api.http"), []byte("GET "+srv.URL+"/x\n"), 0o600)

	p := startACPProc(t, nil)
	sid := p.session(dir)
	id := p.send("session/prompt", acpPrompt(sid, "/find-ceiling api.http start_rps=20 max_rps=60 max_p99=200"), false)

	var kinds []string
	var summary strings.Builder
	var reply map[string]any
	p.until(3*time.Minute, func(m map[string]any) bool {
		if got, ok := m["id"].(float64); ok && int(got) == id && m["method"] == nil {
			reply = m
			return true
		}
		if kind, u := updateOf(m); kind != "" {
			kinds = append(kinds, kind)
			if kind == "agent_message_chunk" {
				summary.WriteString(u["content"].(map[string]any)["text"].(string))
			}
		}
		return false
	})
	if reply["error"] != nil || reply["result"].(map[string]any)["stopReason"] != "end_turn" {
		t.Fatalf("reply = %v", reply)
	}
	for _, want := range []string{"plan", "tool_call", "tool_call_update", "agent_message_chunk"} {
		if !contains(kinds, want) {
			t.Errorf("updates %v lack %s", kinds, want)
		}
	}
	if s := summary.String(); !strings.Contains(s, "60 RPS") || !strings.Contains(s, "ceiling_not_reached") || !strings.Contains(s, "report:") {
		t.Errorf("summary:\n%s", s)
	}
}

func TestAutopilotACP_E2E_CancelStopsARunOnTheDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a daemon and a run")
	}
	isolateCache(t)
	srv := newKneeServer(t, 1_000_000)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "api.http"), []byte("GET "+srv.URL+"/x\n"), 0o600)
	d := startDaemon(t, dir, nil)

	p := startACPProc(t, []string{"GG_MCP_URL=" + d.url})
	sid := p.session(dir)
	id := p.send("session/prompt", acpPrompt(sid, "/find-ceiling api.http start_rps=20 max_rps=600 duration=2m"), false)

	// The start result carries the run's id; cancel once it is running.
	var runID string
	p.until(60*time.Second, func(m map[string]any) bool {
		if kind, u := updateOf(m); kind == "tool_call_update" {
			if out, ok := u["rawOutput"].(map[string]any); ok {
				if v, _ := out["session_id"].(string); v != "" {
					runID = v
				}
			}
		}
		return runID != ""
	})
	time.Sleep(2 * time.Second)
	p.send("session/cancel", map[string]any{"sessionId": sid}, true)

	var reply map[string]any
	p.until(60*time.Second, func(m map[string]any) bool {
		if got, ok := m["id"].(float64); ok && int(got) == id && m["method"] == nil {
			reply = m
			return true
		}
		return false
	})
	if reply["result"].(map[string]any)["stopReason"] != "cancelled" {
		t.Fatalf("reply = %v", reply)
	}

	// The daemon must no longer be running it.
	be, err := agent.NewBackend(agent.BackendOptions{URL: d.url})
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cs, err := be.Client(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "get_live_telemetry", Arguments: map[string]any{"session_id": runID}})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(res.StructuredContent)
		var tel struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(raw, &tel)
		if tel.State == "stopped" || tel.State == "finished" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is still %q after the agent was cancelled", runID, tel.State)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestAutopilotACP_E2E_FreeTextWithoutAKeyAndCleanExit(t *testing.T) {
	p := startACPProc(t, nil)
	sid := p.session(t.TempDir())
	id := p.send("session/prompt", acpPrompt(sid, "please load test my API"), false)
	var text string
	p.until(30*time.Second, func(m map[string]any) bool {
		if kind, u := updateOf(m); kind == "agent_message_chunk" {
			text = u["content"].(map[string]any)["text"].(string)
		}
		if got, ok := m["id"].(float64); ok && int(got) == id && m["method"] == nil {
			return true
		}
		return false
	})
	if !strings.Contains(text, "need an LLM") || !strings.Contains(text, "/find-ceiling") {
		t.Errorf("text = %q", text)
	}
}

// scriptedOpenAI serves an OpenAI-compatible chat endpoint that plays back steps in order.
func scriptedOpenAI(t *testing.T, steps []string) string {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		msg := `{"content":"done"}`
		if i < len(steps) {
			msg = steps[i]
		}
		if req["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s,\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", msg)
			return
		}
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":%s,"finish_reason":"stop"}]}`, msg)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

func callStep(name string, args any) string {
	a, _ := json.Marshal(args)
	arg, _ := json.Marshal(string(a))
	return fmt.Sprintf(`{"content":null,"tool_calls":[{"id":"x-%s","type":"function","function":{"name":%q,"arguments":%s}}]}`, name, name, arg)
}

func TestAutopilotGenerateCLI_E2E_LocalModelCompletesTheDryRunLoop(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
	defer target.Close()
	file := "GET " + target.URL + "/items\n"
	base := scriptedOpenAI(t, []string{
		callStep("validate_http_file", map[string]any{"content": file}),
		callStep("run_journey_dry_run", map[string]any{"content": file}),
		callStep("save_http_file", map[string]any{"content": file}),
		callStep("exit_loop", map[string]any{}),
	})
	out := filepath.Join(t.TempDir(), "requests.http")

	cmd := exec.Command(os.Args[0], "autopilot", "generate", "--base-url", base, "--model", "local-model", "--out", out, "an items API")
	cmd.Env = append(os.Environ(), "GG_TEST_RUN_AS_GG=1", "GEMINI_API_KEY=", "GOOGLE_API_KEY=")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("gg autopilot generate: %v\nstderr:\n%s", err, stderr.String())
	}
	if b, _ := os.ReadFile(out); string(b) != file {
		t.Errorf("saved file = %q", b)
	}
	for _, want := range []string{"validate_http_file", "run_journey_dry_run", "save_http_file", "saved " + out} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("progress lacks %q:\n%s", want, stderr.String())
		}
	}
}

func TestAutopilotRunCLI_WithoutAModelFailsWithAHint(t *testing.T) {
	cmd := exec.Command(os.Args[0], "autopilot", "run", "find the ceiling")
	cmd.Env = append(os.Environ(), "GG_TEST_RUN_AS_GG=1", "GEMINI_API_KEY=", "GOOGLE_API_KEY=", "GOOGLE_GENAI_USE_VERTEXAI=", "GG_AUTOPILOT_BASE_URL=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 1 || !strings.Contains(stderr.String(), "no LLM configured") {
		t.Fatalf("err=%v stderr=%s", err, stderr.String())
	}
}
