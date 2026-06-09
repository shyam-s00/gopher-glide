package hive

import (
	"testing"

	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func jsonDir(varName, pattern string) httpreader.ExportDirective {
	return httpreader.ExportDirective{VarName: varName, Engine: httpreader.ExportEngineJSONPath, Pattern: pattern}
}

func reDir(varName, pattern string) httpreader.ExportDirective {
	return httpreader.ExportDirective{VarName: varName, Engine: httpreader.ExportEngineRegex, Pattern: pattern}
}

// ── Extract – JSONPath happy paths ────────────────────────────────────────────

func TestExtract_JSONPath_TopLevelString(t *testing.T) {
	body := []byte(`{"token": "abc123"}`)
	got, err := Extract(body, jsonDir("t", "$.token"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "abc123" {
		t.Errorf("expected abc123, got %q", got)
	}
}

func TestExtract_JSONPath_NestedPath(t *testing.T) {
	body := []byte(`{"data": {"token": "nested-tok"}}`)
	got, err := Extract(body, jsonDir("t", "$.data.token"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "nested-tok" {
		t.Errorf("expected nested-tok, got %q", got)
	}
}

func TestExtract_JSONPath_DeeplyNested(t *testing.T) {
	body := []byte(`{"a": {"b": {"c": "deep-value"}}}`)
	got, err := Extract(body, jsonDir("v", "$.a.b.c"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "deep-value" {
		t.Errorf("expected deep-value, got %q", got)
	}
}

func TestExtract_JSONPath_IntegerValue(t *testing.T) {
	body := []byte(`{"data": {"id": 42}}`)
	got, err := Extract(body, jsonDir("id", "$.data.id"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "42" {
		t.Errorf("expected 42, got %q", got)
	}
}

func TestExtract_JSONPath_FloatValue(t *testing.T) {
	body := []byte(`{"score": 9.5}`)
	got, err := Extract(body, jsonDir("s", "$.score"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "9.5" {
		t.Errorf("expected 9.5, got %q", got)
	}
}

func TestExtract_JSONPath_BooleanValue(t *testing.T) {
	body := []byte(`{"active": true}`)
	got, err := Extract(body, jsonDir("a", "$.active"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "true" {
		t.Errorf("expected true, got %q", got)
	}
}

func TestExtract_JSONPath_ArrayIndex_First(t *testing.T) {
	body := []byte(`{"items": [{"id": "first"}, {"id": "second"}]}`)
	got, err := Extract(body, jsonDir("id", "$.items[0].id"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "first" {
		t.Errorf("expected first, got %q", got)
	}
}

func TestExtract_JSONPath_ArrayIndex_Second(t *testing.T) {
	body := []byte(`{"items": [{"id": "first"}, {"id": "second"}]}`)
	got, err := Extract(body, jsonDir("id", "$.items[1].id"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "second" {
		t.Errorf("expected second, got %q", got)
	}
}

func TestExtract_JSONPath_TopLevelArrayElement(t *testing.T) {
	body := []byte(`{"list": ["alpha", "beta", "gamma"]}`)
	got, err := Extract(body, jsonDir("v", "$.list[2]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "gamma" {
		t.Errorf("expected gamma, got %q", got)
	}
}

func TestExtract_JSONPath_PathWithoutDollarSign(t *testing.T) {
	body := []byte(`{"token": "bare"}`)
	got, err := Extract(body, jsonDir("t", "token"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "bare" {
		t.Errorf("expected bare, got %q", got)
	}
}

func TestExtract_JSONPath_ObjectLeafSerialised(t *testing.T) {
	body := []byte(`{"meta": {"version": 1, "env": "prod"}}`)
	got, err := Extract(body, jsonDir("m", "$.meta"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty JSON string for object leaf")
	}
}

// ── Extract – JSONPath error paths ────────────────────────────────────────────

func TestExtract_JSONPath_KeyNotFound(t *testing.T) {
	body := []byte(`{"data": {"token": "abc"}}`)
	_, err := Extract(body, jsonDir("x", "$.data.missing"))
	if err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
}

func TestExtract_JSONPath_InvalidJSON(t *testing.T) {
	_, err := Extract([]byte("not-json"), jsonDir("t", "$.token"))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestExtract_JSONPath_NullValue(t *testing.T) {
	_, err := Extract([]byte(`{"token": null}`), jsonDir("t", "$.token"))
	if err == nil {
		t.Fatal("expected error for null value, got nil")
	}
}

func TestExtract_JSONPath_ArrayIndexOutOfRange(t *testing.T) {
	body := []byte(`{"items": [{"id": "only"}]}`)
	_, err := Extract(body, jsonDir("id", "$.items[5].id"))
	if err == nil {
		t.Fatal("expected error for out-of-range index, got nil")
	}
}

func TestExtract_JSONPath_TraverseThroughNonObject(t *testing.T) {
	body := []byte(`{"token": "abc"}`)
	_, err := Extract(body, jsonDir("x", "$.token.sub"))
	if err == nil {
		t.Fatal("expected error when traversing through a string, got nil")
	}
}

func TestExtract_JSONPath_EmptyBody(t *testing.T) {
	_, err := Extract([]byte(""), jsonDir("t", "$.token"))
	if err == nil {
		t.Fatal("expected error for empty body, got nil")
	}
}

// ── Extract – Regex happy paths ───────────────────────────────────────────────

func TestExtract_Regex_CaptureGroup(t *testing.T) {
	body := []byte("session_id=abc123xyz")
	got, err := Extract(body, reDir("s", "session_id=([a-zA-Z0-9]+)"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "abc123xyz" {
		t.Errorf("expected abc123xyz, got %q", got)
	}
}

func TestExtract_Regex_NoCaptureGroup_ReturnsFullMatch(t *testing.T) {
	body := []byte("token-xyz789")
	got, err := Extract(body, reDir("t", "token-[a-z0-9]+"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "token-xyz789" {
		t.Errorf("expected token-xyz789, got %q", got)
	}
}

func TestExtract_Regex_MultilineBody(t *testing.T) {
	body := []byte("HTTP/1.1 200 OK\r\nSet-Cookie: session_id=cafebabe; Path=/\r\n")
	got, err := Extract(body, reDir("s", "session_id=([a-zA-Z0-9]+)"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cafebabe" {
		t.Errorf("expected cafebabe, got %q", got)
	}
}

func TestExtract_Regex_FirstMatchReturned(t *testing.T) {
	body := []byte("id=one id=two id=three")
	got, err := Extract(body, reDir("id", "id=([a-z]+)"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "one" {
		t.Errorf("expected one, got %q", got)
	}
}

// ── Extract – Regex error paths ───────────────────────────────────────────────

func TestExtract_Regex_NoMatch(t *testing.T) {
	_, err := Extract([]byte(`{"response": "ok"}`), reDir("s", "session_id=([a-zA-Z0-9]+)"))
	if err == nil {
		t.Fatal("expected error for no match, got nil")
	}
}

func TestExtract_Regex_InvalidPattern(t *testing.T) {
	_, err := Extract([]byte("anything"), reDir("x", "[invalid"))
	if err == nil {
		t.Fatal("expected error for invalid regex, got nil")
	}
}

// ── Extract – unknown engine ──────────────────────────────────────────────────

func TestExtract_UnknownEngine(t *testing.T) {
	d := httpreader.ExportDirective{VarName: "t", Engine: "xpath", Pattern: "//token"}
	_, err := Extract([]byte(`{"token":"abc"}`), d)
	if err == nil {
		t.Fatal("expected error for unknown engine, got nil")
	}
}

// ── Internal path helpers ─────────────────────────────────────────────────────

func TestSplitPath(t *testing.T) {
	cases := []struct {
		path string
		want []string
	}{
		{"token", []string{"token"}},
		{"data.token", []string{"data", "token"}},
		{"a.b.c", []string{"a", "b", "c"}},
		{"items[0].id", []string{"items[0]", "id"}},
	}
	for _, c := range cases {
		got := splitPath(c.path)
		if len(got) != len(c.want) {
			t.Errorf("splitPath(%q): expected %v, got %v", c.path, c.want, got)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("splitPath(%q)[%d]: expected %q, got %q", c.path, i, c.want[i], got[i])
			}
		}
	}
}

func TestParseArraySegment(t *testing.T) {
	cases := []struct {
		seg     string
		wantIdx int
		wantKey string
		wantArr bool
	}{
		{"items[0]", 0, "items", true},
		{"[2]", 2, "", true},
		{"users[10]", 10, "users", true},
		{"token", 0, "", false},
		{"items[]", 0, "", false},
		{"items[abc]", 0, "", false},
	}
	for _, c := range cases {
		idx, key, isArr := parseArraySegment(c.seg)
		if isArr != c.wantArr {
			t.Errorf("parseArraySegment(%q): isArr=%v, want %v", c.seg, isArr, c.wantArr)
			continue
		}
		if isArr {
			if idx != c.wantIdx {
				t.Errorf("parseArraySegment(%q): idx=%d, want %d", c.seg, idx, c.wantIdx)
			}
			if key != c.wantKey {
				t.Errorf("parseArraySegment(%q): key=%q, want %q", c.seg, key, c.wantKey)
			}
		}
	}
}

func TestValueToString(t *testing.T) {
	cases := []struct {
		input   interface{}
		want    string
		wantErr bool
	}{
		{"hello", "hello", false},
		{float64(42), "42", false},
		{float64(3.14), "3.14", false},
		{true, "true", false},
		{false, "false", false},
		{nil, "", true},
	}
	for _, c := range cases {
		got, err := valueToString(c.input)
		if c.wantErr {
			if err == nil {
				t.Errorf("valueToString(%v): expected error, got nil", c.input)
			}
		} else {
			if err != nil {
				t.Errorf("valueToString(%v): unexpected error: %v", c.input, err)
			}
			if got != c.want {
				t.Errorf("valueToString(%v): expected %q, got %q", c.input, c.want, got)
			}
		}
	}
}
