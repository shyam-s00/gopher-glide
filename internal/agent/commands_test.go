package agent

import (
	"reflect"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
)

func TestParseSlash(t *testing.T) {
	cases := []struct {
		in   string
		name string
		pos  []string
		kv   map[string]string
		ok   bool
	}{
		{"/find-ceiling api.http max_p99=200", "find-ceiling", []string{"api.http"}, map[string]string{"max_p99": "200"}, true},
		{"  /DRY-RUN  a.http  ", "dry-run", []string{"a.http"}, map[string]string{}, true},
		{"/slo-guard file=x.http max-error-rate=0.02 profile=load", "slo-guard", nil, map[string]string{"file": "x.http", "max_error_rate": "0.02", "profile": "load"}, true},
		{"/triage base.snap cur.snap", "triage", []string{"base.snap", "cur.snap"}, map[string]string{}, true},
		{"run a test", "", nil, nil, false},
		{"/", "", nil, nil, false},
		{"", "", nil, nil, false},
	}
	for _, c := range cases {
		name, a, ok := ParseSlash(c.in)
		if ok != c.ok || name != c.name {
			t.Errorf("%q: name=%q ok=%v", c.in, name, ok)
			continue
		}
		if ok && (!reflect.DeepEqual(a.Pos, c.pos) || !reflect.DeepEqual(a.Named, c.kv)) {
			t.Errorf("%q: pos=%v named=%v", c.in, a.Pos, a.Named)
		}
	}
}

func TestArgs_NumbersAreValidated(t *testing.T) {
	a := Args{Named: map[string]string{"max_rps": "abc", "max_p99": "-1", "ok": "250"}}
	if _, err := a.int("max_rps"); err == nil {
		t.Error("non-numeric int accepted")
	}
	if _, err := a.float("max_p99"); err == nil {
		t.Error("negative float accepted")
	}
	if v, err := a.float("ok"); err != nil || v != 250 {
		t.Errorf("ok = %v %v", v, err)
	}
	if v, err := a.int("missing"); err != nil || v != 0 {
		t.Errorf("missing = %v %v", v, err)
	}
}

func TestArgs_ResolveMakesPathsAbsoluteButLeavesTags(t *testing.T) {
	a := Args{Pos: []string{"api.http", "release-1"}, Named: map[string]string{"file": "sub/x.http", "baseline": "snaps/a.snap", "current": "tagname", "max_p99": "9"}}
	r := a.Resolve("/work")
	if r.Named["file"] != "/work/sub/x.http" || r.Named["baseline"] != "/work/snaps/a.snap" || r.Named["current"] != "tagname" || r.Named["max_p99"] != "9" {
		t.Errorf("named = %v", r.Named)
	}
	if !reflect.DeepEqual(r.Pos, []string{"/work/api.http", "release-1"}) {
		t.Errorf("pos = %v", r.Pos)
	}
	if abs := (Args{Pos: []string{"/etc/x.http"}}).Resolve("/work"); abs.Pos[0] != "/etc/x.http" {
		t.Errorf("absolute path changed: %v", abs.Pos)
	}
}

func TestIsCommand(t *testing.T) {
	for _, n := range []string{"dry-run", "find-ceiling", "slo-guard", "triage"} {
		if !IsCommand(n) {
			t.Errorf("%s not a command", n)
		}
	}
	if IsCommand("generate") || IsCommand("") {
		t.Error("generate is LLM-backed and not a deterministic command")
	}
}

func TestConverter_DoesNotRepeatTextThatStreamed(t *testing.T) {
	var got []Event
	c := &converter{sink: func(e Event) { got = append(got, e) }}
	part := func(txt string, partial bool) *session.Event {
		return &session.Event{LLMResponse: model.LLMResponse{Partial: partial, Content: &genai.Content{Parts: []*genai.Part{{Text: txt}}}}}
	}
	c.convert(part("Hel", true))
	c.convert(part("lo", true))
	if final := c.convert(part("Hello", false)); final != "Hello" {
		t.Errorf("final = %q", final)
	}
	if len(got) != 2 || got[0].Text != "Hel" || got[1].Text != "lo" {
		t.Fatalf("events = %+v", got)
	}
	// A later turn that never streamed is shown once.
	got = nil
	c.convert(part("again", false))
	if len(got) != 1 || got[0].Text != "again" {
		t.Errorf("events = %+v", got)
	}
}

func TestConverter_ThoughtsToolsAndFlattenedResults(t *testing.T) {
	var got []Event
	c := &converter{sink: func(e Event) { got = append(got, e) }}
	c.convert(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
		{Text: "thinking", Thought: true},
		{FunctionCall: &genai.FunctionCall{ID: "1", Name: "snap_list", Args: map[string]any{"limit": 1}}},
		{FunctionResponse: &genai.FunctionResponse{ID: "1", Name: "snap_list", Response: map[string]any{"output": map[string]any{"total": 3}}}},
	}}}})
	if len(got) != 3 || got[0].Kind != KindThought || got[1].Kind != KindToolCall || got[2].Kind != KindToolResult {
		t.Fatalf("events = %+v", got)
	}
	if got[2].Result["total"] != 3 {
		t.Errorf("result not flattened: %v", got[2].Result)
	}
}

func TestUpdateMapper_PairsResultsWithCallsAndMarksFailures(t *testing.T) {
	m := &updateMapper{}
	start := m.updates(Event{Kind: KindToolCall, Tool: "steer_traffic", Args: map[string]any{"action": "mark"}})
	if len(start) != 1 || start[0].ToolCall == nil || start[0].ToolCall.Title != "steer_traffic mark" {
		t.Fatalf("start = %+v", start)
	}
	id := start[0].ToolCall.ToolCallId
	done := m.updates(Event{Kind: KindToolResult, Tool: "steer_traffic", Result: map[string]any{"error": "boom"}})
	if len(done) != 1 || done[0].ToolCallUpdate == nil || done[0].ToolCallUpdate.ToolCallId != id || *done[0].ToolCallUpdate.Status != "failed" {
		t.Fatalf("done = %+v", done)
	}
	if extra := m.updates(Event{Kind: KindToolResult, Tool: "steer_traffic"}); extra != nil {
		t.Errorf("a result with no open call produced %v", extra)
	}
}

func TestModelConfig_Selection(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "")
	if HasModel(ModelConfig{}) {
		t.Error("nothing configured should mean no model")
	}
	if _, err := NewModel(t.Context(), ModelConfig{}); err != ErrNoModel {
		t.Errorf("err = %v", err)
	}
	if _, err := NewModel(t.Context(), ModelConfig{BaseURL: "http://x/v1"}); err == nil {
		t.Error("--base-url without --model should fail")
	}
	m, err := NewModel(t.Context(), ModelConfig{BaseURL: "http://localhost:11434/v1", Name: "qwen3"})
	if err != nil || m.Name() != "qwen3" {
		t.Errorf("openai-compatible model: %v %v", m, err)
	}
	t.Setenv("GEMINI_API_KEY", "k")
	g, err := NewModel(t.Context(), ModelConfig{})
	if err != nil || g.Name() != DefaultModel {
		t.Errorf("gemini default: %v %v", g, err)
	}
	t.Setenv(EnvModel, "gemini-x")
	if mc := ModelConfigFromEnv(ModelConfig{}); mc.Name != "gemini-x" {
		t.Errorf("env model = %q", mc.Name)
	}
	if mc := ModelConfigFromEnv(ModelConfig{Name: "flag"}); mc.Name != "flag" {
		t.Errorf("flag should win over env, got %q", mc.Name)
	}
}

func TestRunTracker_FollowsStartsAndStopsThroughADKEnvelope(t *testing.T) {
	tr := newRunTracker(nil)
	resp := func(name, id string) *session.Event {
		return &session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{Name: name, Response: map[string]any{"output": map[string]any{"session_id": id}}}},
		}}}}
	}
	tr.observe(resp("start_simulation", "a"))
	tr.observe(resp("start_simulation", "b"))
	tr.observe(resp("get_live_telemetry", "a"))
	tr.observe(resp("stop_simulation", "a"))
	tr.observe(nil)
	if _, ok := tr.live["b"]; !ok || len(tr.live) != 1 {
		t.Errorf("live = %v, want only b", tr.live)
	}
}
