package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

// openAIModel is a model.LLM over the OpenAI-compatible chat completions API
// (Ollama, vLLM, LM Studio, OpenAI). It is small on purpose: the official SDK
// would add far more to the binary than the three calls gg needs.
type openAIModel struct {
	name   string
	url    string
	key    string
	client *http.Client
}

func newOpenAIModel(name, baseURL, key string, client *http.Client) *openAIModel {
	if client == nil {
		client = &http.Client{}
	}
	return &openAIModel{name: name, url: strings.TrimRight(baseURL, "/") + "/chat/completions", key: key, client: client}
}

func (m *openAIModel) Name() string { return m.name }

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model         string         `json:"model"`
	Messages      []oaMessage    `json:"messages"`
	Tools         []oaTool       `json:"tools,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions map[string]any `json:"stream_options,omitempty"`
	Temperature   *float32       `json:"temperature,omitempty"`
	TopP          *float32       `json:"top_p,omitempty"`
	MaxTokens     int32          `json:"max_tokens,omitempty"`
}

type oaChoice struct {
	Message struct {
		Content   *string      `json:"content"`
		ToolCalls []oaToolCall `json:"tool_calls"`
	} `json:"message"`
	Delta struct {
		Content   *string      `json:"content"`
		ToolCalls []oaToolCall `json:"tool_calls"`
	} `json:"delta"`
	FinishReason string `json:"finish_reason"`
}

type oaResponse struct {
	Choices []oaChoice `json:"choices"`
	Usage   *struct {
		Prompt     int32 `json:"prompt_tokens"`
		Completion int32 `json:"completion_tokens"`
		Total      int32 `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (m *openAIModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		body, err := m.buildRequest(req, stream)
		if err != nil {
			yield(nil, err)
			return
		}
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.url, bytes.NewReader(body))
		if err != nil {
			yield(nil, err)
			return
		}
		hreq.Header.Set("Content-Type", "application/json")
		if m.key != "" {
			hreq.Header.Set("Authorization", "Bearer "+m.key)
		}
		resp, err := m.client.Do(hreq)
		if err != nil {
			yield(nil, fmt.Errorf("model request: %w", err))
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode/100 != 2 {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			yield(nil, fmt.Errorf("model server returned %s: %s", resp.Status, strings.TrimSpace(string(msg))))
			return
		}
		if stream {
			m.readStream(resp.Body, yield)
			return
		}
		var r oaResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&r); err != nil {
			yield(nil, fmt.Errorf("decode model response: %w", err))
			return
		}
		out, err := finalResponse(&r)
		yield(out, err)
	}
}

func (m *openAIModel) buildRequest(req *model.LLMRequest, stream bool) ([]byte, error) {
	r := oaRequest{Model: m.name, Stream: stream}
	if stream {
		r.StreamOptions = map[string]any{"include_usage": true}
	}
	if cfg := req.Config; cfg != nil {
		r.Temperature, r.TopP, r.MaxTokens = cfg.Temperature, cfg.TopP, cfg.MaxOutputTokens
		if cfg.SystemInstruction != nil {
			if t := contentText(cfg.SystemInstruction); t != "" {
				r.Messages = append(r.Messages, oaMessage{Role: "system", Content: &t})
			}
		}
		for _, t := range cfg.Tools {
			for _, d := range t.FunctionDeclarations {
				tool, err := toOATool(d)
				if err != nil {
					return nil, err
				}
				r.Tools = append(r.Tools, tool)
			}
		}
	}
	msgs, err := toOAMessages(req.Contents)
	if err != nil {
		return nil, err
	}
	r.Messages = append(r.Messages, msgs...)
	return json.Marshal(r)
}

func contentText(c *genai.Content) string {
	var parts []string
	for _, p := range c.Parts {
		if p.Text != "" && !p.Thought {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func toOATool(d *genai.FunctionDeclaration) (oaTool, error) {
	var t oaTool
	t.Type = "function"
	t.Function.Name, t.Function.Description = d.Name, d.Description
	switch {
	case d.ParametersJsonSchema != nil:
		t.Function.Parameters = d.ParametersJsonSchema
	case d.Parameters != nil:
		raw, err := json.Marshal(d.Parameters)
		if err != nil {
			return t, fmt.Errorf("tool %s schema: %w", d.Name, err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return t, fmt.Errorf("tool %s schema: %w", d.Name, err)
		}
		t.Function.Parameters = lowerTypes(v)
	default:
		t.Function.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return t, nil
}

// lowerTypes turns Gemini's upper-case schema types (OBJECT, STRING) into JSON Schema's.
func lowerTypes(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && k == "type" {
				x[k] = strings.ToLower(s)
				continue
			}
			x[k] = lowerTypes(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = lowerTypes(e)
		}
	}
	return v
}

// toOAMessages converts the conversation. Calls the model made without an id
// get a synthetic one that the matching result reuses, in order per tool name.
func toOAMessages(contents []*genai.Content) ([]oaMessage, error) {
	var out []oaMessage
	pending := map[string][]string{}
	n := 0
	for _, c := range contents {
		if c == nil {
			continue
		}
		if c.Role == "model" {
			msg := oaMessage{Role: "assistant"}
			if t := contentText(c); t != "" {
				msg.Content = &t
			}
			for _, p := range c.Parts {
				fc := p.FunctionCall
				if fc == nil {
					continue
				}
				args, err := json.Marshal(fc.Args)
				if err != nil {
					return nil, fmt.Errorf("arguments of %s: %w", fc.Name, err)
				}
				if fc.Args == nil {
					args = []byte("{}")
				}
				id := fc.ID
				if id == "" {
					n++
					id = fmt.Sprintf("call_%d", n)
					pending[fc.Name] = append(pending[fc.Name], id)
				}
				call := oaToolCall{ID: id, Type: "function"}
				call.Function.Name, call.Function.Arguments = fc.Name, string(args)
				msg.ToolCalls = append(msg.ToolCalls, call)
			}
			if msg.Content != nil || len(msg.ToolCalls) > 0 {
				out = append(out, msg)
			}
			continue
		}
		var text []string
		for _, p := range c.Parts {
			switch {
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				raw, err := json.Marshal(fr.Response)
				if err != nil {
					return nil, fmt.Errorf("result of %s: %w", fr.Name, err)
				}
				s := string(raw)
				id := fr.ID
				if id == "" {
					if q := pending[fr.Name]; len(q) > 0 {
						id, pending[fr.Name] = q[0], q[1:]
					}
				}
				out = append(out, oaMessage{Role: "tool", ToolCallID: id, Content: &s})
			case p.Text != "" && !p.Thought:
				text = append(text, p.Text)
			}
		}
		if len(text) > 0 {
			t := strings.Join(text, "\n")
			out = append(out, oaMessage{Role: "user", Content: &t})
		}
	}
	return out, nil
}

// readStream turns the server-sent stream into partial text events and one final event.
func (m *openAIModel) readStream(body io.Reader, yield func(*model.LLMResponse, error) bool) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	acc := &streamAcc{}
	var last oaResponse
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk oaResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			yield(nil, fmt.Errorf("decode model stream: %w", err))
			return
		}
		if chunk.Error != nil {
			yield(nil, fmt.Errorf("model server: %s", chunk.Error.Message))
			return
		}
		if chunk.Usage != nil {
			last.Usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != "" {
			acc.finish = ch.FinishReason
		}
		if t := ch.Delta.Content; t != nil && *t != "" {
			acc.text.WriteString(*t)
			part := &genai.Part{Text: *t}
			if !yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}, Partial: true}, nil) {
				return
			}
		}
		for _, tc := range ch.Delta.ToolCalls {
			acc.addCall(tc)
		}
	}
	if err := sc.Err(); err != nil {
		yield(nil, fmt.Errorf("read model stream: %w", err))
		return
	}
	final := oaResponse{Usage: last.Usage}
	final.Choices = []oaChoice{{FinishReason: acc.finish}}
	text := acc.text.String()
	if text != "" {
		final.Choices[0].Message.Content = &text
	}
	final.Choices[0].Message.ToolCalls = acc.calls
	out, err := finalResponse(&final)
	yield(out, err)
}

type streamAcc struct {
	text   strings.Builder
	calls  []oaToolCall
	finish string
}

// addCall merges one streamed tool-call fragment: by index when the server
// sends one, else a fragment with an id starts a new call and one without continues the last.
func (a *streamAcc) addCall(tc oaToolCall) {
	i := -1
	switch {
	case tc.Index != nil && *tc.Index >= 0 && *tc.Index < len(a.calls):
		i = *tc.Index
	case tc.ID == "" && len(a.calls) > 0:
		i = len(a.calls) - 1
	}
	if i < 0 {
		a.calls = append(a.calls, oaToolCall{ID: tc.ID, Type: "function"})
		i = len(a.calls) - 1
	}
	c := &a.calls[i]
	if tc.ID != "" {
		c.ID = tc.ID
	}
	if tc.Function.Name != "" {
		c.Function.Name = tc.Function.Name
	}
	c.Function.Arguments += tc.Function.Arguments
}

// finalResponse builds the complete model turn from a decoded response.
func finalResponse(r *oaResponse) (*model.LLMResponse, error) {
	if r.Error != nil {
		return nil, fmt.Errorf("model server: %s", r.Error.Message)
	}
	if len(r.Choices) == 0 {
		return nil, errors.New("model server returned no choices")
	}
	ch := r.Choices[0]
	content := &genai.Content{Role: "model"}
	if t := ch.Message.Content; t != nil && *t != "" {
		content.Parts = append(content.Parts, &genai.Part{Text: *t})
	}
	for i, tc := range ch.Message.ToolCalls {
		args := map[string]any{}
		if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
			if err := json.Unmarshal([]byte(s), &args); err != nil {
				return nil, fmt.Errorf("model sent invalid arguments for %s: %w", tc.Function.Name, err)
			}
		}
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i+1)
		}
		content.Parts = append(content.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: tc.Function.Name, Args: args}})
	}
	out := &model.LLMResponse{Content: content, TurnComplete: true, FinishReason: finishReason(ch.FinishReason)}
	if u := r.Usage; u != nil {
		out.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: u.Prompt, CandidatesTokenCount: u.Completion, TotalTokenCount: u.Total,
		}
	}
	return out, nil
}

func finishReason(s string) genai.FinishReason {
	switch s {
	case "length":
		return genai.FinishReasonMaxTokens
	case "content_filter":
		return genai.FinishReasonSafety
	case "":
		return ""
	}
	return genai.FinishReasonStop
}
