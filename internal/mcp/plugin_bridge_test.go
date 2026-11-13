package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
)

const pluginToken = "plugin-token-0123456789"

type sseEvent struct {
	Name string
	Env  session.Envelope
	Raw  string
}

// pluginEvents reads GET /plugin/events on a goroutine; events arrive on ch.
type pluginEvents struct {
	ch     chan sseEvent
	status int
	header http.Header
	closed chan struct{}
}

func openPluginEvents(t *testing.T, base string) *pluginEvents {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+PluginEventsPath, nil)
	req.Header.Set("Authorization", "Bearer "+pluginToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	pe := &pluginEvents{ch: make(chan sseEvent, 4096), status: resp.StatusCode, header: resp.Header, closed: make(chan struct{})}
	go func() {
		defer close(pe.closed)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev := sseEvent{Name: name, Raw: strings.TrimPrefix(line, "data: ")}
				_ = json.Unmarshal([]byte(ev.Raw), &ev.Env)
				pe.ch <- ev
			}
		}
		close(pe.ch)
	}()
	return pe
}

// waitFor returns the first event named name that satisfies ok.
func (pe *pluginEvents) waitFor(t *testing.T, name string, ok func(sseEvent) bool) sseEvent {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, open := <-pe.ch:
			if !open {
				t.Fatalf("event stream ended while waiting for %q", name)
			}
			if ev.Name == name && (ok == nil || ok(ev)) {
				return ev
			}
		case <-timeout:
			t.Fatalf("no %q event within 10s", name)
		}
	}
}

func lcp(t *testing.T, ev sseEvent) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(ev.Env.LCP, &m); err != nil {
		t.Fatalf("lcp: %v: %s", err, ev.Raw)
	}
	return m
}

func postJSON(t *testing.T, url string, body any, token string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	if s, ok := body.(string); ok {
		b = []byte(s)
	}
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func pluginStartBody() map[string]any {
	return map[string]any{"http_content": validHTTP, "profile": "load", "peak_rps": 20, "duration": "30s"}
}

func baseOf(url string) string { return strings.TrimSuffix(url, EndpointPath) }

// Model A, flow 1: an agent starts a run; the plugin watches it and stops it.
func TestPluginBridge_AgentRunIsSeenAndStoppedByThePlugin(t *testing.T) {
	url, _, _ := httpServer(t, "normal", HTTPOptions{Token: pluginToken})
	base := baseOf(url)
	agent, err := httpClient(t, url, pluginToken)
	if err != nil {
		t.Fatal(err)
	}
	events := openPluginEvents(t, base)

	var started StartOutput
	if res := call(t, agent, "start_simulation", startArgs(), &started); res.IsError {
		t.Fatal(errText(res))
	}
	if started.Origin != session.OriginAgent {
		t.Errorf("origin = %q", started.Origin)
	}

	st := events.waitFor(t, "started", nil)
	if st.Env.SessionID != started.SessionID || st.Env.Origin != session.OriginAgent {
		t.Fatalf("started envelope = %s", st.Raw)
	}
	events.waitFor(t, "heartbeat", nil)

	status, ack := postJSON(t, base+PluginControlPath, map[string]any{"session_id": started.SessionID, "id": "p1", "command": "stop"}, pluginToken)
	if status != 200 || ack["event"] != "ack" || ack["id"] != "p1" {
		t.Fatalf("stop: %d %v", status, ack)
	}
	stopped := events.waitFor(t, "stopped", nil)
	if stopped.Env.SessionID != started.SessionID {
		t.Errorf("stopped for %s", stopped.Env.SessionID)
	}

	var tel session.Telemetry
	call(t, agent, "get_live_telemetry", map[string]any{"session_id": started.SessionID}, &tel)
	if tel.State != session.Stopped {
		t.Errorf("agent sees state %s after the plugin's stop", tel.State)
	}
}

// Model A, flow 2: the plugin starts a run; the agent reads it and may steer only once allowed.
func TestPluginBridge_UserRunIsReadOnlyForTheAgentUntilAllowed(t *testing.T) {
	url, _, _ := httpServer(t, "normal", HTTPOptions{Token: pluginToken})
	base := baseOf(url)
	agent, err := httpClient(t, url, pluginToken)
	if err != nil {
		t.Fatal(err)
	}
	events := openPluginEvents(t, base)

	status, started := postJSON(t, base+PluginStartPath, pluginStartBody(), pluginToken)
	if status != 200 || started["origin"] != "user" {
		t.Fatalf("start: %d %v", status, started)
	}
	id := started["session_id"].(string)
	events.waitFor(t, "started", func(e sseEvent) bool { return e.Env.Origin == session.OriginUser })

	var tel session.Telemetry
	if res := call(t, agent, "get_live_telemetry", map[string]any{}, &tel); res.IsError {
		t.Fatalf("telemetry on a user run must work: %s", errText(res))
	}
	if tel.SessionID != id || tel.Origin != session.OriginUser || tel.AgentSteerAllowed == nil || *tel.AgentSteerAllowed {
		t.Fatalf("telemetry = %+v", tel)
	}

	mark := map[string]any{"session_id": id, "action": "mark", "label": "agent was here"}
	if res := call(t, agent, "steer_traffic", mark, nil); !res.IsError || !strings.Contains(errText(res), "user_session_locked") {
		t.Fatalf("steer on a locked user run: error=%v %q", res.IsError, errText(res))
	}
	if res := call(t, agent, "stop_simulation", map[string]any{"session_id": id}, nil); !res.IsError || !strings.Contains(errText(res), "user_session_locked") {
		t.Fatalf("stop on a locked user run: error=%v %q", res.IsError, errText(res))
	}

	status, ack := postJSON(t, base+PluginControlPath, map[string]any{"session_id": id, "command": "agent_steer", "allow": true}, pluginToken)
	if status != 200 || ack["allow"] != true || ack["command"] != "agent_steer" {
		t.Fatalf("agent_steer: %d %v", status, ack)
	}
	events.waitFor(t, "ack", func(e sseEvent) bool { return lcp(t, e)["command"] == "agent_steer" })

	var steer SteerOutput
	if res := call(t, agent, "steer_traffic", mark, &steer); res.IsError {
		t.Fatalf("steer after the user allowed it: %s", errText(res))
	}
	if steer.Origin != session.OriginUser {
		t.Errorf("steer result origin = %q", steer.Origin)
	}
	m := events.waitFor(t, "mark", func(e sseEvent) bool { return lcp(t, e)["label"] == "agent was here" })
	if lcp(t, m)["source"] != "agent" {
		t.Errorf("the plugin must see who marked: %s", m.Raw)
	}

	postJSON(t, base+PluginControlPath, map[string]any{"session_id": id, "command": "agent_steer", "allow": false}, pluginToken)
	if res := call(t, agent, "steer_traffic", mark, nil); !res.IsError || !strings.Contains(errText(res), "user_session_locked") {
		t.Fatalf("steer after the user closed it again: %q", errText(res))
	}
}

func TestPluginBridge_AgentSteersOnAUserRunStayWithinMaxRPS(t *testing.T) {
	l := DefaultLimits()
	l.MaxRPS = 50
	url, _, _ := httpServerLimits(t, "normal", HTTPOptions{Token: pluginToken}, l)
	base := baseOf(url)
	agent, _ := httpClient(t, url, pluginToken)

	body := pluginStartBody()
	body["allow_agent_steer"] = true
	if status, out := postJSON(t, base+PluginStartPath, body, pluginToken); status != 200 {
		t.Fatalf("start: %d %v", status, out)
	}
	res := call(t, agent, "steer_traffic", map[string]any{"action": "hold_rps", "target_rps": 500}, nil)
	if !res.IsError || !strings.Contains(errText(res), "--max-rps") {
		t.Fatalf("an allowed agent is still clamped: %q", errText(res))
	}
}

func TestPluginBridge_UserRunAboveMaxRPSStartsButAnAgentRunDoesNot(t *testing.T) {
	l := DefaultLimits()
	l.MaxRPS = 50
	url, _, _ := httpServerLimits(t, "normal", HTTPOptions{Token: pluginToken}, l)
	base := baseOf(url)
	agent, _ := httpClient(t, url, pluginToken)

	over := pluginStartBody()
	over["peak_rps"] = 400
	over["duration"] = "20m"
	if status, out := postJSON(t, base+PluginStartPath, over, pluginToken); status != 200 {
		t.Fatalf("a user run is not held to the agent caps: %d %v", status, out)
	}
	args := startArgs()
	args["peak_rps"] = 400
	if res := call(t, agent, "start_simulation", args, nil); !res.IsError || !strings.Contains(errText(res), "--max-rps") {
		t.Fatalf("an agent run above the cap must be refused: %q", errText(res))
	}
}

func TestPluginBridge_EmptySessionIDMeansTheAgentsOwnRun(t *testing.T) {
	url, _, _ := httpServer(t, "normal", HTTPOptions{Token: pluginToken})
	base := baseOf(url)
	agent, _ := httpClient(t, url, pluginToken)

	_, user := postJSON(t, base+PluginStartPath, pluginStartBody(), pluginToken)
	var own StartOutput
	call(t, agent, "start_simulation", startArgs(), &own)

	var tel session.Telemetry
	call(t, agent, "get_live_telemetry", map[string]any{}, &tel)
	if tel.SessionID != own.SessionID || tel.SessionID == user["session_id"] {
		t.Fatalf("empty id resolved to %s, want the agent's %s", tel.SessionID, own.SessionID)
	}

	// The plugin's empty id means its own run.
	status, ack := postJSON(t, base+PluginControlPath, map[string]any{"command": "mark", "label": "mine"}, pluginToken)
	if status != 200 || ack["event"] != "mark" {
		t.Fatalf("plugin mark: %d %v", status, ack)
	}
	var userTel session.Telemetry
	call(t, agent, "get_live_telemetry", map[string]any{"session_id": user["session_id"]}, &userTel)
	if len(userTel.Marks) != 1 || userTel.Marks[0].Label != "mine" {
		t.Errorf("the mark landed elsewhere: user marks %v", userTel.Marks)
	}
}

func TestPluginBridge_SlotsAreSeparate(t *testing.T) {
	url, _, _ := httpServer(t, "normal", HTTPOptions{Token: pluginToken})
	base := baseOf(url)
	agent, _ := httpClient(t, url, pluginToken)

	if status, out := postJSON(t, base+PluginStartPath, pluginStartBody(), pluginToken); status != 200 {
		t.Fatalf("first user run: %d %v", status, out)
	}
	status, out := postJSON(t, base+PluginStartPath, pluginStartBody(), pluginToken)
	if status != 409 || out["reason"] != "session_limit" || !strings.Contains(out["message"].(string), "--max-user-sessions") {
		t.Fatalf("second user run: %d %v", status, out)
	}
	for i := 0; i < 2; i++ {
		if res := call(t, agent, "start_simulation", startArgs(), nil); res.IsError {
			t.Fatalf("agent run %d must not be blocked by the user's run: %s", i, errText(res))
		}
	}
}

func TestPluginBridge_ControlErrorsAreLCPErrorEvents(t *testing.T) {
	url, _, _ := httpServer(t, "reject-bias", HTTPOptions{Token: pluginToken})
	base := baseOf(url)
	_, started := postJSON(t, base+PluginStartPath, pluginStartBody(), pluginToken)
	id := started["session_id"]
	ctl := base + PluginControlPath

	cases := []struct {
		name   string
		body   any
		status int
		reason string
	}{
		{"not json", "{nope", 400, "invalid_argument"},
		{"not an object", "[1]", 400, "invalid_argument"},
		{"unknown session", map[string]any{"session_id": "sim_nope", "command": "release"}, 404, "unknown_session"},
		{"unknown command", map[string]any{"session_id": id, "command": "shutdown"}, 400, "unknown_command"},
		{"agent_steer needs allow", map[string]any{"session_id": id, "command": "agent_steer"}, 400, "invalid_argument"},
		{"the child's own refusal is a reply", map[string]any{"session_id": id, "id": "r", "command": "bias", "amount": 5}, 200, "invalid_argument"},
	}
	for _, c := range cases {
		status, out := postJSON(t, ctl, c.body, pluginToken)
		if status != c.status || out["event"] != "error" || out["reason"] != c.reason {
			t.Errorf("%s: %d %v", c.name, status, out)
		}
	}

	postJSON(t, ctl, map[string]any{"session_id": id, "command": "stop"}, pluginToken)
	eventually := time.After(5 * time.Second)
	for {
		status, out := postJSON(t, ctl, map[string]any{"session_id": id, "command": "release"}, pluginToken)
		if status == 409 && out["reason"] == "session_not_running" {
			break
		}
		select {
		case <-eventually:
			t.Fatalf("a stopped run must answer 409 session_not_running, last: %d %v", status, out)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestPluginBridge_StartValidationAndConfigPath(t *testing.T) {
	url, _, _ := httpServer(t, "normal", HTTPOptions{Token: pluginToken})
	base := baseOf(url)
	start := base + PluginStartPath

	if status, out := postJSON(t, start, "{nope", pluginToken); status != 400 || out["reason"] != "invalid_argument" {
		t.Errorf("bad body: %d %v", status, out)
	}
	if status, out := postJSON(t, start, map[string]any{"http_content": validHTTP}, pluginToken); status != 400 || !strings.Contains(out["message"].(string), "load shape") {
		t.Errorf("no shape: %d %v", status, out)
	}
	if status, out := postJSON(t, start, map[string]any{"config_path": "/no/such/gg.yaml"}, pluginToken); status != 400 || !strings.Contains(out["message"].(string), "config_path") {
		t.Errorf("missing config: %d %v", status, out)
	}

	dir := t.TempDir()
	httpFile := filepath.Join(dir, "r.http")
	cfgFile := filepath.Join(dir, "gg.yaml")
	if err := os.WriteFile(httpFile, []byte(validHTTP), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := "config:\n  httpFile: " + httpFile + "\nstages:\n  - duration: 2h\n    target_rps: 5000\n"
	if err := os.WriteFile(cfgFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	status, out := postJSON(t, start, map[string]any{"config_path": cfgFile}, pluginToken)
	if status != 200 || out["origin"] != "user" || out["peak_rps"] != float64(5000) {
		t.Fatalf("a config on disk runs as it is, beyond the agent caps: %d %v", status, out)
	}
}

func TestPluginBridge_EndpointsShareTheMCPGuard(t *testing.T) {
	url, _, _ := httpServer(t, "normal", HTTPOptions{Token: pluginToken})
	base := baseOf(url)

	for _, path := range []string{PluginEventsPath, PluginControlPath, PluginStartPath} {
		req, _ := http.NewRequest(http.MethodPost, base+path, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%s without a token: %d", path, resp.StatusCode)
		}

		req, _ = http.NewRequest(http.MethodPost, base+path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+pluginToken)
		req.Header.Set("Origin", "https://evil.example")
		if resp, err = http.DefaultClient.Do(req); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Errorf("%s from a web page: %d", path, resp.StatusCode)
		}

		req, _ = http.NewRequest(http.MethodPost, base+path, strings.NewReader("{}"))
		req.Host = "evil.example"
		req.Header.Set("Authorization", "Bearer "+pluginToken)
		if resp, err = http.DefaultClient.Do(req); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Errorf("%s with a rebinding Host: %d", path, resp.StatusCode)
		}
	}
}

func TestPluginBridge_WrongMethodsAreRefused(t *testing.T) {
	url, _, _ := httpServer(t, "normal", HTTPOptions{})
	base := baseOf(url)
	for path, method := range map[string]string{PluginEventsPath: http.MethodPost, PluginControlPath: http.MethodGet, PluginStartPath: http.MethodGet} {
		req, _ := http.NewRequest(method, base+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d", method, path, resp.StatusCode)
		}
	}
}

func TestPluginBridge_StreamEndsCleanlyWhenTheDaemonStops(t *testing.T) {
	url, _, stop := httpServer(t, "normal", HTTPOptions{Token: pluginToken})
	base := baseOf(url)
	events := openPluginEvents(t, base)
	if events.header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", events.header.Get("Content-Type"))
	}
	postJSON(t, base+PluginStartPath, pluginStartBody(), pluginToken)
	events.waitFor(t, "heartbeat", nil)

	begin := time.Now()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	events.waitFor(t, "stopped", nil)
	select {
	case <-events.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream must end once the daemon shuts down")
	}
	if d := time.Since(begin); d > 8*time.Second {
		t.Errorf("shutdown took %s; open streams must not hold it for the whole grace period", d)
	}
}

func TestPluginBridge_LateSubscriberCatchesUp(t *testing.T) {
	url, _, _ := httpServer(t, "normal", HTTPOptions{Token: pluginToken})
	base := baseOf(url)
	agent, _ := httpClient(t, url, pluginToken)
	var started StartOutput
	call(t, agent, "start_simulation", startArgs(), &started)
	call(t, agent, "steer_traffic", map[string]any{"action": "mark", "label": "before the panel opened"}, nil)

	events := openPluginEvents(t, base)
	st := events.waitFor(t, "started", nil)
	if st.Env.SessionID != started.SessionID {
		t.Errorf("replayed started = %s", st.Raw)
	}
	m := events.waitFor(t, "mark", nil)
	if lcp(t, m)["label"] != "before the panel opened" {
		t.Errorf("replayed mark = %s", m.Raw)
	}
	events.waitFor(t, "heartbeat", nil)
}
