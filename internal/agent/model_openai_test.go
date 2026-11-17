package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

func oaServer(t *testing.T, h func(w http.ResponseWriter, body map[string]any)) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("bad request json: %v", err)
		}
		got = append(got, body)
		h(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func collect(t *testing.T, m model.LLM, req *model.LLMRequest, stream bool) ([]*model.LLMResponse, error) {
	t.Helper()
	var out []*model.LLMResponse
	for r, err := range m.GenerateContent(context.Background(), req, stream) {
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

func userReq(text string) *model.LLMRequest {
	return &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText(text, genai.RoleUser)}}
}

func TestOpenAI_NonStreamToolCall(t *testing.T) {
	srv, got := oaServer(t, func(w http.ResponseWriter, _ map[string]any) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"snap_list","arguments":"{\"limit\":2}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	})
	m := newOpenAIModel("qwen", srv.URL+"/v1", "k", nil)
	req := userReq("hi")
	req.Config = &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText("be brief", genai.RoleUser),
		Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name: "snap_list", Description: "list",
			Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{"limit": {Type: genai.TypeInteger}}},
		}}}},
	}
	rs, err := collect(t, m, req, false)
	if err != nil || len(rs) != 1 {
		t.Fatalf("rs=%d err=%v", len(rs), err)
	}
	fc := rs[0].Content.Parts[0].FunctionCall
	if fc == nil || fc.Name != "snap_list" || fc.ID != "c1" || fc.Args["limit"] != float64(2) {
		t.Fatalf("function call = %+v", fc)
	}
	if rs[0].UsageMetadata.TotalTokenCount != 7 || !rs[0].TurnComplete {
		t.Errorf("usage/turn = %+v", rs[0])
	}

	body := (*got)[0]
	msgs := body["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["role"] != "user" {
		t.Errorf("messages = %v", msgs)
	}
	params := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	if params["type"] != "object" || params["properties"].(map[string]any)["limit"].(map[string]any)["type"] != "integer" {
		t.Errorf("schema types not lower-cased: %v", params)
	}
}

func TestOpenAI_StreamTextThenFinal(t *testing.T) {
	srv, got := oaServer(t, func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range []string{
			`{"choices":[{"delta":{"content":"Hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
		} {
			_, _ = io.WriteString(w, "data: "+l+"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	rs, err := collect(t, newOpenAIModel("m", srv.URL+"/v1/", "", nil), userReq("hi"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 3 || !rs[0].Partial || !rs[1].Partial || rs[2].Partial {
		t.Fatalf("want 2 partials + 1 final, got %d", len(rs))
	}
	if rs[0].Content.Parts[0].Text != "Hel" || rs[2].Content.Parts[0].Text != "Hello" || !rs[2].TurnComplete {
		t.Errorf("text = %q / %q", rs[0].Content.Parts[0].Text, rs[2].Content.Parts[0].Text)
	}
	if rs[2].UsageMetadata == nil || rs[2].UsageMetadata.TotalTokenCount != 3 {
		t.Errorf("usage = %+v", rs[2].UsageMetadata)
	}
	if (*got)[0]["stream"] != true {
		t.Error("stream flag not sent")
	}
}

func TestOpenAI_StreamToolCallFragments(t *testing.T) {
	srv, _ := oaServer(t, func(w http.ResponseWriter, _ map[string]any) {
		for _, l := range []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"start_simulation","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"snap\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"true}"}},{"index":1,"id":"b","function":{"name":"snap_list","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		} {
			_, _ = io.WriteString(w, "data: "+l+"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	rs, err := collect(t, newOpenAIModel("m", srv.URL+"/v1", "", nil), userReq("go"), true)
	if err != nil || len(rs) != 1 {
		t.Fatalf("rs=%d err=%v", len(rs), err)
	}
	parts := rs[0].Content.Parts
	if len(parts) != 2 || parts[0].FunctionCall.Name != "start_simulation" || parts[0].FunctionCall.Args["snap"] != true ||
		parts[1].FunctionCall.ID != "b" {
		t.Fatalf("parts = %+v %+v", parts[0].FunctionCall, parts[1].FunctionCall)
	}
}

func TestOpenAI_ToolResultsReuseSyntheticIDs(t *testing.T) {
	msgs, err := toOAMessages([]*genai.Content{
		genai.NewContentFromText("run it", genai.RoleUser),
		{Role: "model", Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "a"}},
			{FunctionCall: &genai.FunctionCall{Name: "b", Args: map[string]any{"x": 1}}},
		}},
		{Role: "user", Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{Name: "b", Response: map[string]any{"r": "B"}}},
			{FunctionResponse: &genai.FunctionResponse{Name: "a", Response: map[string]any{"r": "A"}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 || msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 2 {
		t.Fatalf("messages = %+v", msgs)
	}
	idA, idB := msgs[1].ToolCalls[0].ID, msgs[1].ToolCalls[1].ID
	if idA == "" || idA == idB || msgs[1].ToolCalls[0].Function.Arguments != "{}" {
		t.Errorf("call ids/args = %q %q %q", idA, idB, msgs[1].ToolCalls[0].Function.Arguments)
	}
	if msgs[2].ToolCallID != idB || msgs[3].ToolCallID != idA || !strings.Contains(*msgs[2].Content, `"B"`) {
		t.Errorf("results = %+v %+v", msgs[2], msgs[3])
	}
}

func TestOpenAI_Errors(t *testing.T) {
	cases := []struct {
		name string
		h    func(w http.ResponseWriter, _ map[string]any)
		want string
	}{
		{"http status", func(w http.ResponseWriter, _ map[string]any) {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `model "x" not found`)
		}, `404 Not Found: model "x" not found`},
		{"no choices", func(w http.ResponseWriter, _ map[string]any) { _, _ = io.WriteString(w, `{"choices":[]}`) }, "no choices"},
		{"bad arguments", func(w http.ResponseWriter, _ map[string]any) {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"id":"1","function":{"name":"t","arguments":"{nope"}}]}}]}`)
		}, "invalid arguments for t"},
		{"error object", func(w http.ResponseWriter, _ map[string]any) {
			_, _ = io.WriteString(w, `{"error":{"message":"overloaded"}}`)
		}, "overloaded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := oaServer(t, c.h)
			_, err := collect(t, newOpenAIModel("m", srv.URL+"/v1", "", nil), userReq("x"), false)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestOpenAI_ContextCancelStopsStream(t *testing.T) {
	release := make(chan struct{})
	srv, _ := oaServer(t, func(w http.ResponseWriter, _ map[string]any) {
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"a"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-release
	})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newOpenAIModel("m", srv.URL+"/v1", "", nil)
	n := 0
	var gotErr error
	for _, err := range m.GenerateContent(ctx, userReq("x"), true) {
		if err != nil {
			gotErr = err
			break
		}
		n++
		cancel()
	}
	if n != 1 || gotErr == nil {
		t.Fatalf("n=%d err=%v", n, gotErr)
	}
}
