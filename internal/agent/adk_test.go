package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ggmcp "github.com/shyam-s00/gopher-glide/internal/mcp"
)

// scriptedModel is an OpenAI-compatible server that answers each request with the next scripted step.
type scriptedModel struct {
	mu    sync.Mutex
	steps []func(req map[string]any) string // returns a message JSON object
	calls int
	reqs  []map[string]any
}

func (s *scriptedModel) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		s.mu.Lock()
		i := s.calls
		s.calls++
		s.reqs = append(s.reqs, req)
		s.mu.Unlock()
		msg := `{"content":"done"}`
		if i < len(s.steps) {
			msg = s.steps[i](req)
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

func toolCall(name string, args any) func(map[string]any) string {
	return func(map[string]any) string {
		a, _ := json.Marshal(args)
		arg, _ := json.Marshal(string(a))
		return fmt.Sprintf(`{"content":null,"tool_calls":[{"id":"t-%s","type":"function","function":{"name":%q,"arguments":%s}}]}`, name, name, arg)
	}
}

const goodHTTP = "GET {{base}}/items\n"

func newTarget(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func testBackend(t *testing.T) *Backend {
	t.Helper()
	b, err := NewBackend(BackendOptions{Limits: ggmcp.DefaultLimits(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

func TestGenerate_LoopAuthorsVerifiesAndSaves(t *testing.T) {
	target := newTarget(t, 200)
	file := "GET " + target + "/items\n"
	sm := &scriptedModel{steps: []func(map[string]any) string{
		toolCall("validate_http_file", map[string]any{"content": file}),
		toolCall("run_journey_dry_run", map[string]any{"content": file}),
		toolCall("save_http_file", map[string]any{"content": file}),
		toolCall("exit_loop", map[string]any{}),
	}}
	url := sm.serve(t)
	llm, err := NewModel(context.Background(), ModelConfig{Name: "local", BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "sub", "requests.http")
	var events []Event
	res, err := Run(context.Background(), Options{Model: llm, Backend: testBackend(t), OutPath: out}, ModeGenerate,
		"items API at "+target, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Saved != out {
		t.Fatalf("saved = %q", res.Saved)
	}
	if b, _ := os.ReadFile(out); string(b) != file {
		t.Errorf("file = %q", b)
	}

	var names []string
	for _, e := range events {
		if e.Kind == KindToolCall {
			names = append(names, e.Tool)
		}
	}
	if got := strings.Join(names, ","); got != "validate_http_file,run_journey_dry_run,save_http_file,exit_loop" {
		t.Errorf("tool sequence = %s", got)
	}
	// The dry-run result came back through the real tools and reached the model.
	var sawDryRun bool
	for _, e := range events {
		if e.Kind == KindToolResult && e.Tool == "run_journey_dry_run" {
			sawDryRun = true
			if r, _ := e.Result["result"].(map[string]any); r["ok"] != true {
				t.Errorf("dry run result = %v", e.Result)
			}
		}
	}
	if !sawDryRun {
		t.Error("no dry-run result event")
	}
	if len(sm.reqs[0]["tools"].([]any)) != 4 {
		t.Errorf("model was offered %d tools, want validate, dry-run, save and exit_loop", len(sm.reqs[0]["tools"].([]any)))
	}
}

func TestGenerate_NeverPassingFailsAfterMaxTurns(t *testing.T) {
	sm := &scriptedModel{steps: nil} // always answers "done" without saving
	url := sm.serve(t)
	llm, _ := NewModel(context.Background(), ModelConfig{Name: "local", BaseURL: url})
	out := filepath.Join(t.TempDir(), "r.http")
	res, err := Run(context.Background(), Options{Model: llm, Backend: testBackend(t), OutPath: out, MaxTurns: 3}, ModeGenerate, "x", nil)
	if err == nil || !strings.Contains(err.Error(), "after 3 turns") {
		t.Fatalf("err = %v", err)
	}
	if res.Saved != "" {
		t.Errorf("saved = %q", res.Saved)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("file written despite no pass")
	}
	if sm.calls != 3 {
		t.Errorf("model called %d times, want one per turn (3)", sm.calls)
	}
}

func TestGenerate_SaveRefusesInvalidFile(t *testing.T) {
	sm := &scriptedModel{steps: []func(map[string]any) string{
		toolCall("save_http_file", map[string]any{"content": "GET not a url\n"}),
		toolCall("exit_loop", map[string]any{}),
	}}
	llm, _ := NewModel(context.Background(), ModelConfig{Name: "local", BaseURL: sm.serve(t)})
	out := filepath.Join(t.TempDir(), "r.http")
	_, err := Run(context.Background(), Options{Model: llm, Backend: testBackend(t), OutPath: out}, ModeGenerate, "x", nil)
	if err == nil {
		t.Fatal("want an error when nothing valid was saved")
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("an invalid file was saved")
	}
}

func TestRun_ToolOffers(t *testing.T) {
	for mode, want := range map[Mode][]string{
		ModeRun:    {"start_simulation", "stop_simulation", "steer_traffic", "get_live_telemetry"},
		ModeTriage: {"snap_diff", "snap_assert", "snap_list"},
	} {
		sm := &scriptedModel{}
		llm, _ := NewModel(context.Background(), ModelConfig{Name: "local", BaseURL: sm.serve(t)})
		if _, err := Run(context.Background(), Options{Model: llm, Backend: testBackend(t)}, mode, "go", nil); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		offered := map[string]bool{}
		for _, tl := range sm.reqs[0]["tools"].([]any) {
			offered[tl.(map[string]any)["function"].(map[string]any)["name"].(string)] = true
		}
		for _, n := range want {
			if !offered[n] {
				t.Errorf("%s: tool %s not offered (have %v)", mode, n, offered)
			}
		}
		if mode == ModeTriage && (offered["start_simulation"] || offered["steer_traffic"]) {
			t.Errorf("triage must be read-only, offered %v", offered)
		}
	}
}

func TestRun_RejectsBadInput(t *testing.T) {
	if _, err := Run(context.Background(), Options{}, ModeRun, "x", nil); err == nil {
		t.Error("no model/backend should fail")
	}
	sm := &scriptedModel{}
	llm, _ := NewModel(context.Background(), ModelConfig{Name: "l", BaseURL: sm.serve(t)})
	if _, err := Run(context.Background(), Options{Model: llm, Backend: testBackend(t)}, ModeRun, "  ", nil); err == nil {
		t.Error("empty task should fail")
	}
	if _, err := Run(context.Background(), Options{Model: llm, Backend: testBackend(t)}, Mode("nope"), "x", nil); err == nil {
		t.Error("unknown mode should fail")
	}
}

func TestRun_CancelReturnsPromptly(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	llm, _ := NewModel(context.Background(), ModelConfig{Name: "l", BaseURL: srv.URL + "/v1"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, Options{Model: llm, Backend: testBackend(t)}, ModeRun, "x", nil)
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("want an error after cancel")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// blockingModel answers nothing until block closes, and signals started on the first request.
func blockingModel(t *testing.T, block <-chan struct{}, started chan<- struct{}) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}
