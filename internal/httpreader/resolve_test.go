package httpreader

import (
	"net/http"
	"testing"
)

// ── ToHTTPRequest — basic method & URL ───────────────────────────────────────

func TestToHTTPRequest_GETNoBody(t *testing.T) {
	spec := &RequestSpec{
		Method:  "GET",
		URL:     "https://example.com/api",
		Headers: make(http.Header),
	}

	req, err := spec.ToHTTPRequest(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Method != "GET" {
		t.Errorf("expected method=GET, got %s", req.Method)
	}
	if req.URL.String() != "https://example.com/api" {
		t.Errorf("expected url=https://example.com/api, got %s", req.URL.String())
	}
}

func TestToHTTPRequest_POSTWithBody(t *testing.T) {
	spec := &RequestSpec{
		Method:  "POST",
		URL:     "https://example.com/api",
		Headers: make(http.Header),
		Body:    `{"key":"value"}`,
	}

	req, err := spec.ToHTTPRequest(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Method != "POST" {
		t.Errorf("expected method=POST, got %s", req.Method)
	}
	if req.Body == nil {
		t.Fatal("expected non-nil body")
	}
}

// ── ToHTTPRequest — headers are copied ───────────────────────────────────────

func TestToHTTPRequest_HeadersCopied(t *testing.T) {
	spec := &RequestSpec{
		Method:  "GET",
		URL:     "https://example.com",
		Headers: make(http.Header),
	}
	spec.Headers.Set("Accept", "application/json")
	spec.Headers.Set("X-Request-ID", "gg-001")

	req, err := spec.ToHTTPRequest(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Header.Get("Accept") != "application/json" {
		t.Errorf("expected Accept=application/json, got %s", req.Header.Get("Accept"))
	}
	if req.Header.Get("X-Request-ID") != "gg-001" {
		t.Errorf("expected X-Request-ID=gg-001, got %s", req.Header.Get("X-Request-ID"))
	}
}

// ── ToHTTPRequest — variable substitution in URL ─────────────────────────────

func TestToHTTPRequest_VarSubstitutionInURL(t *testing.T) {
	spec := &RequestSpec{
		Method:  "GET",
		URL:     "https://{{host}}/api/{{version}}/users",
		Headers: make(http.Header),
	}
	vars := map[string]string{
		"host":    "example.com",
		"version": "v2",
	}

	req, err := spec.ToHTTPRequest(vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "https://example.com/api/v2/users"
	if req.URL.String() != expected {
		t.Errorf("expected url=%s, got %s", expected, req.URL.String())
	}
}

func TestToHTTPRequest_VarSubstitutionInBody(t *testing.T) {
	spec := &RequestSpec{
		Method:  "POST",
		URL:     "https://example.com/api",
		Headers: make(http.Header),
		Body:    `{"userId": "{{userId}}", "name": "{{name}}"}`,
	}
	vars := map[string]string{
		"userId": "42",
		"name":   "gopher",
	}

	req, err := spec.ToHTTPRequest(vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Body == nil {
		t.Fatal("expected non-nil body")
	}
	buf := make([]byte, 512)
	n, _ := req.Body.Read(buf)
	got := string(buf[:n])
	expected := `{"userId": "42", "name": "gopher"}`
	if got != expected {
		t.Errorf("expected body=%s, got %s", expected, got)
	}
}

func TestToHTTPRequest_VarSubstitutionInHeaders(t *testing.T) {
	spec := &RequestSpec{
		Method:  "GET",
		URL:     "https://example.com",
		Headers: make(http.Header),
	}
	spec.Headers.Set("Authorization", "Bearer {{token}}")
	vars := map[string]string{"token": "secret123"}

	req, err := spec.ToHTTPRequest(vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Header.Get("Authorization") != "Bearer secret123" {
		t.Errorf("expected Authorization=Bearer secret123, got %s", req.Header.Get("Authorization"))
	}
}

func TestToHTTPRequest_UnresolvedPlaceholdersRemainAsIs(t *testing.T) {
	spec := &RequestSpec{
		Method:  "GET",
		URL:     "https://example.com/{{unknown}}",
		Headers: make(http.Header),
	}

	req, err := spec.ToHTTPRequest(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// net/http percent-encodes { and } in the path; compare via the decoded path.
	if req.URL.Host != "example.com" {
		t.Errorf("expected host=example.com, got %s", req.URL.Host)
	}
	decoded, decErr := req.URL.EscapedPath(), req.URL.RawPath
	_ = decoded
	_ = decErr
	// The raw path should still contain the encoded placeholder characters.
	if req.URL.Path != "/{{unknown}}" {
		t.Errorf("expected decoded path=/{{unknown}}, got %s", req.URL.Path)
	}
}

func TestToHTTPRequest_NilVarsMap(t *testing.T) {
	spec := &RequestSpec{
		Method:  "GET",
		URL:     "https://example.com/api",
		Headers: make(http.Header),
	}
	// Should not panic when vars is nil
	req, err := spec.ToHTTPRequest(nil)
	if err != nil {
		t.Fatalf("unexpected error with nil vars: %v", err)
	}
	if req == nil {
		t.Fatal("expected non-nil request")
	}
}

func TestToHTTPRequest_EmptyVarsMap(t *testing.T) {
	spec := &RequestSpec{
		Method:  "GET",
		URL:     "https://example.com/api",
		Headers: make(http.Header),
	}
	req, err := spec.ToHTTPRequest(map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error with empty vars: %v", err)
	}
	if req == nil {
		t.Fatal("expected non-nil request")
	}
}

// ── ToHTTPRequest — invalid URL ───────────────────────────────────────────────

func TestToHTTPRequest_InvalidURL(t *testing.T) {
	spec := &RequestSpec{
		Method:  "GET",
		URL:     "://bad-url",
		Headers: make(http.Header),
	}
	_, err := spec.ToHTTPRequest(nil)
	if err == nil {
		t.Fatal("expected error for invalid URL, got nil")
	}
}

// ── Round-trip: Parse → ToHTTPRequest ────────────────────────────────────────

func TestRoundTrip_ParseThenToHTTPRequest(t *testing.T) {
	content := `### Create Post
POST https://httpbin.org/post
Content-Type: application/json
Accept: application/json
X-Client-Name: gg/1.0

{"title": "test", "userId": 1}
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}

	req, err := specs[0].ToHTTPRequest(nil)
	if err != nil {
		t.Fatalf("ToHTTPRequest error: %v", err)
	}

	if req.Method != "POST" {
		t.Errorf("expected method=POST, got %s", req.Method)
	}
	if req.URL.Host != "httpbin.org" {
		t.Errorf("expected host=httpbin.org, got %s", req.URL.Host)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type=application/json, got %s", req.Header.Get("Content-Type"))
	}
	if req.Header.Get("X-Client-Name") != "gg/1.0" {
		t.Errorf("expected X-Client-Name=gg/1.0, got %s", req.Header.Get("X-Client-Name"))
	}
	if req.Body == nil {
		t.Fatal("expected non-nil body after round-trip")
	}
}

func TestRoundTrip_ParseThenToHTTPRequest_WithVarSubstitution(t *testing.T) {
	content := `### Dynamic request
GET https://{{baseURL}}/users/{{userId}}
Accept: application/json
X-Trace-ID: {{traceId}}
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	vars := map[string]string{
		"baseURL": "api.example.com",
		"userId":  "99",
		"traceId": "trace-abc",
	}

	req, err := specs[0].ToHTTPRequest(vars)
	if err != nil {
		t.Fatalf("ToHTTPRequest error: %v", err)
	}

	if req.URL.String() != "https://api.example.com/users/99" {
		t.Errorf("unexpected URL: %s", req.URL.String())
	}
	if req.Header.Get("X-Trace-ID") != "trace-abc" {
		t.Errorf("expected X-Trace-ID=trace-abc, got %s", req.Header.Get("X-Trace-ID"))
	}
}
