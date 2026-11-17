package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	ggmcp "github.com/shyam-s00/gopher-glide/internal/mcp"
)

// wire is a bare NDJSON JSON-RPC client for the ACP server, so the tests see the bytes a real client would.
type wire struct {
	t      *testing.T
	in     io.WriteCloser
	mu     sync.Mutex
	nextID int
	resp   map[int]chan map[string]any
	notes  chan map[string]any
	done   chan struct{}
}

func startACP(t *testing.T, o ACPOptions) *wire {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = ServeACP(ctx, inR, outW, o)
	}()
	w := &wire{t: t, in: inW, resp: map[int]chan map[string]any{}, notes: make(chan map[string]any, 256), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) != nil {
				t.Errorf("non-JSON line on the protocol stream: %q", sc.Text())
				continue
			}
			if id, ok := m["id"].(float64); ok && m["method"] == nil {
				w.mu.Lock()
				ch := w.resp[int(id)]
				w.mu.Unlock()
				if ch != nil {
					ch <- m
				}
				continue
			}
			w.notes <- m
		}
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		cancel()
		select {
		case <-served:
		case <-time.After(15 * time.Second):
			t.Error("ACP server did not stop")
		}
		_ = outW.Close()
	})
	return w
}

func (w *wire) send(method string, params any) chan map[string]any {
	w.mu.Lock()
	w.nextID++
	id := w.nextID
	ch := make(chan map[string]any, 1)
	w.resp[id] = ch
	w.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if _, err := w.in.Write(append(b, '\n')); err != nil {
		w.t.Fatalf("write %s: %v", method, err)
	}
	return ch
}

func (w *wire) call(method string, params any) map[string]any {
	w.t.Helper()
	select {
	case r := <-w.send(method, params):
		return r
	case <-time.After(30 * time.Second):
		w.t.Fatalf("no reply to %s", method)
	}
	return nil
}

func (w *wire) notify(method string, params any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	_, _ = w.in.Write(append(b, '\n'))
}

// updates reads session/update notifications until stop returns true.
func (w *wire) until(stop func(kind string, u map[string]any) bool) []map[string]any {
	w.t.Helper()
	var seen []map[string]any
	timeout := time.After(30 * time.Second)
	for {
		select {
		case n := <-w.notes:
			if n["method"] != "session/update" {
				continue
			}
			u := n["params"].(map[string]any)["update"].(map[string]any)
			seen = append(seen, u)
			if stop(u["sessionUpdate"].(string), u) {
				return seen
			}
		case <-timeout:
			w.t.Fatalf("timed out; saw %d updates", len(seen))
		}
	}
}

func (w *wire) newSession(cwd string) string {
	w.t.Helper()
	r := w.call("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	if r["error"] != nil {
		w.t.Fatalf("session/new: %v", r["error"])
	}
	return r["result"].(map[string]any)["sessionId"].(string)
}

func (w *wire) initialize() map[string]any {
	w.t.Helper()
	r := w.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}})
	if r["error"] != nil {
		w.t.Fatalf("initialize: %v", r["error"])
	}
	return r["result"].(map[string]any)
}

func promptParams(sid, text string) map[string]any {
	return map[string]any{"sessionId": sid, "prompt": []any{map[string]any{"type": "text", "text": text}}}
}

func commandNames(u map[string]any) []string {
	var names []string
	for _, c := range u["availableCommands"].([]any) {
		names = append(names, c.(map[string]any)["name"].(string))
	}
	return names
}

func noModelOpts(t *testing.T) ACPOptions {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv("GG_SNAP_DIR", t.TempDir())
	return ACPOptions{Version: "test", Limits: ggmcp.DefaultLimits()}
}

func TestACP_InitializeAdvertisesV1(t *testing.T) {
	w := startACP(t, noModelOpts(t))
	res := w.initialize()
	if res["protocolVersion"] != float64(1) {
		t.Errorf("protocolVersion = %v", res["protocolVersion"])
	}
	info := res["agentInfo"].(map[string]any)
	if info["name"] != "gg-autopilot" || info["version"] != "test" {
		t.Errorf("agentInfo = %v", info)
	}
	if res["authMethods"] == nil {
		t.Error("authMethods must be an array, not null")
	}
}

func TestACP_NewSessionAdvertisesCommandsWithoutAnyKey(t *testing.T) {
	w := startACP(t, noModelOpts(t))
	w.initialize()
	w.newSession(t.TempDir())
	var names []string
	w.until(func(kind string, u map[string]any) bool {
		if kind == "available_commands_update" {
			names = commandNames(u)
			return true
		}
		return false
	})
	// No LLM and no snapshots: /generate and /triage are withheld.
	if got := strings.Join(names, ","); got != "dry-run,find-ceiling,slo-guard" {
		t.Errorf("commands = %s", got)
	}
}

func TestACP_GenerateAdvertisedOnlyWithAModel(t *testing.T) {
	o := noModelOpts(t)
	o.Model = ModelConfig{Name: "m", BaseURL: "http://127.0.0.1:1/v1"}
	w := startACP(t, o)
	w.initialize()
	w.newSession(t.TempDir())
	var names []string
	w.until(func(kind string, u map[string]any) bool {
		if kind == "available_commands_update" {
			names = commandNames(u)
		}
		return names != nil
	})
	if names[len(names)-1] != "generate" {
		t.Errorf("commands = %v, want /generate last", names)
	}
}

func TestACP_DryRunSlashCommandStreamsPlanToolCardsAndSummary(t *testing.T) {
	target := newTarget(t, 200)
	dir := t.TempDir()
	writeFile(t, dir, "api.http", "GET "+target+"/items\n")
	w := startACP(t, noModelOpts(t))
	w.initialize()
	sid := w.newSession(dir)

	reply := w.send("session/prompt", promptParams(sid, "/dry-run api.http"))
	var kinds []string
	var text strings.Builder
	var statuses []string
	updates := w.until(func(kind string, u map[string]any) bool {
		kinds = append(kinds, kind)
		switch kind {
		case "agent_message_chunk":
			text.WriteString(u["content"].(map[string]any)["text"].(string))
		case "tool_call_update":
			statuses = append(statuses, u["status"].(string))
		}
		return kind == "agent_message_chunk"
	})
	_ = updates
	r := <-reply
	if r["error"] != nil || r["result"].(map[string]any)["stopReason"] != "end_turn" {
		t.Fatalf("prompt result = %v", r)
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"plan", "tool_call", "tool_call_update", "agent_message_chunk"} {
		if !strings.Contains(joined, want) {
			t.Errorf("updates %q lack %s", joined, want)
		}
	}
	if !strings.Contains(text.String(), "all steps passed") {
		t.Errorf("summary = %q", text.String())
	}
	if len(statuses) != 1 || statuses[0] != "completed" {
		t.Errorf("tool statuses = %v", statuses)
	}
}

func TestACP_DryRunWithoutAFileExplainsInsteadOfFailing(t *testing.T) {
	w := startACP(t, noModelOpts(t))
	w.initialize()
	sid := w.newSession(t.TempDir())
	reply := w.send("session/prompt", promptParams(sid, "/dry-run"))
	var text string
	w.until(func(kind string, u map[string]any) bool {
		if kind == "agent_message_chunk" {
			text = u["content"].(map[string]any)["text"].(string)
		}
		return text != ""
	})
	if r := <-reply; r["error"] != nil || !strings.Contains(text, "usage: /dry-run") {
		t.Fatalf("reply=%v text=%q", r, text)
	}
}

func TestACP_UnknownCommandListsTheRealOnes(t *testing.T) {
	w := startACP(t, noModelOpts(t))
	w.initialize()
	sid := w.newSession(t.TempDir())
	reply := w.send("session/prompt", promptParams(sid, "/nope"))
	var text string
	w.until(func(kind string, u map[string]any) bool {
		if kind == "agent_message_chunk" {
			text = u["content"].(map[string]any)["text"].(string)
		}
		return text != ""
	})
	<-reply
	if !strings.Contains(text, "Unknown command `/nope`") || !strings.Contains(text, "/find-ceiling") {
		t.Errorf("text = %q", text)
	}
}

func TestACP_FreeTextWithoutAModelPointsAtTheCommands(t *testing.T) {
	w := startACP(t, noModelOpts(t))
	w.initialize()
	sid := w.newSession(t.TempDir())
	reply := w.send("session/prompt", promptParams(sid, "how fast can my API go?"))
	var text string
	w.until(func(kind string, u map[string]any) bool {
		if kind == "agent_message_chunk" {
			text = u["content"].(map[string]any)["text"].(string)
		}
		return text != ""
	})
	r := <-reply
	if r["error"] != nil || r["result"].(map[string]any)["stopReason"] != "end_turn" {
		t.Fatalf("reply = %v", r)
	}
	if !strings.Contains(text, "need an LLM") || !strings.Contains(text, "/dry-run") {
		t.Errorf("text = %q", text)
	}
}

func TestACP_FreeTextWithAModelStreamsThoughtsToolCardsAndAnswer(t *testing.T) {
	sm := &scriptedModel{steps: []func(map[string]any) string{
		toolCall("snap_list", map[string]any{"limit": 1}),
		func(map[string]any) string { return `{"content":"No snapshots yet."}` },
	}}
	o := noModelOpts(t)
	o.Model = ModelConfig{Name: "local", BaseURL: sm.serve(t)}
	w := startACP(t, o)
	w.initialize()
	sid := w.newSession(t.TempDir())
	reply := w.send("session/prompt", promptParams(sid, "what snapshots do I have?"))
	var kinds []string
	w.until(func(kind string, u map[string]any) bool {
		kinds = append(kinds, kind)
		return kind == "agent_message_chunk"
	})
	if r := <-reply; r["result"].(map[string]any)["stopReason"] != "end_turn" {
		t.Fatalf("reply = %v", r)
	}
	if j := strings.Join(kinds, ","); !strings.Contains(j, "tool_call") || !strings.Contains(j, "tool_call_update") {
		t.Errorf("updates = %s", j)
	}
}

func TestACP_CancelEndsTheTurnWithCancelled(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	srv := blockingModel(t, block, started)
	o := noModelOpts(t)
	o.Model = ModelConfig{Name: "local", BaseURL: srv}
	w := startACP(t, o)
	w.initialize()
	sid := w.newSession(t.TempDir())
	reply := w.send("session/prompt", promptParams(sid, "think hard"))
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the model was never called")
	}
	w.notify("session/cancel", map[string]any{"sessionId": sid})
	select {
	case r := <-reply:
		if r["result"].(map[string]any)["stopReason"] != "cancelled" {
			t.Fatalf("reply = %v", r)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("prompt did not return after cancel")
	}
	close(block)
}

func TestACP_UnknownSessionIsAnError(t *testing.T) {
	w := startACP(t, noModelOpts(t))
	w.initialize()
	r := w.call("session/prompt", promptParams("nope", "/dry-run x"))
	if r["error"] == nil {
		t.Fatalf("want an error, got %v", r)
	}
}

func TestACP_SecondPromptWhileRunningIsRefused(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	o := noModelOpts(t)
	o.Model = ModelConfig{Name: "local", BaseURL: blockingModel(t, block, started)}
	w := startACP(t, o)
	w.initialize()
	sid := w.newSession(t.TempDir())
	first := w.send("session/prompt", promptParams(sid, "one"))
	<-started
	r := w.call("session/prompt", promptParams(sid, "two"))
	if r["error"] == nil {
		t.Errorf("second prompt should be refused, got %v", r)
	}
	w.notify("session/cancel", map[string]any{"sessionId": sid})
	<-first
	close(block)
}

func TestACP_GatewayURLFromTheClientIsUsedWhenNoneIsConfigured(t *testing.T) {
	url, token := ggServerFrom(nil)
	if url != "" || token != "" {
		t.Fatalf("empty = %q %q", url, token)
	}
}
