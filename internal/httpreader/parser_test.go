package httpreader

import (
	"os"
	"path/filepath"
	"testing"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func writeHTTPFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.http")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp http file: %v", err)
	}
	return path
}

// ── ParseFile ─────────────────────────────────────────────────────────────────

func TestParseFile_ValidFile(t *testing.T) {
	content := `### Simple GET
GET https://example.com/api
Accept: application/json
`
	path := writeHTTPFile(t, content)
	specs, err := ParseFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(specs))
	}
}

func TestParseFile_NotFound(t *testing.T) {
	_, err := ParseFile("/nonexistent/path/test.http")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// ── Parse — single request ────────────────────────────────────────────────────

func TestParse_SingleGET(t *testing.T) {
	content := `### Get Users
GET https://httpbin.org/get
Accept: application/json
X-Request-ID: gg-001
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(specs))
	}

	r := specs[0]
	if r.Name != "Get Users" {
		t.Errorf("expected name=%q, got %q", "Get Users", r.Name)
	}
	if r.Method != "GET" {
		t.Errorf("expected method=GET, got %s", r.Method)
	}
	if r.URL != "https://httpbin.org/get" {
		t.Errorf("expected url=https://httpbin.org/get, got %s", r.URL)
	}
	if r.Headers.Get("Accept") != "application/json" {
		t.Errorf("expected Accept header=application/json, got %s", r.Headers.Get("Accept"))
	}
	if r.Headers.Get("X-Request-ID") != "gg-001" {
		t.Errorf("expected X-Request-ID=gg-001, got %s", r.Headers.Get("X-Request-ID"))
	}
	if r.Body != "" {
		t.Errorf("expected empty body, got %q", r.Body)
	}
}

func TestParse_SinglePOSTWithBody(t *testing.T) {
	content := `### Create Post
POST https://httpbin.org/post
Content-Type: application/json
Accept: application/json

{"title": "test", "userId": 1}
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(specs))
	}

	r := specs[0]
	if r.Method != "POST" {
		t.Errorf("expected method=POST, got %s", r.Method)
	}
	if r.Headers.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type=application/json, got %s", r.Headers.Get("Content-Type"))
	}
	if r.Body != `{"title": "test", "userId": 1}` {
		t.Errorf("unexpected body: %q", r.Body)
	}
}

// ── Parse — multiple requests ─────────────────────────────────────────────────

func TestParse_MultipleRequests(t *testing.T) {
	content := `### Get Users
GET https://httpbin.org/get
Accept: application/json

### Create Post
POST https://httpbin.org/post
Content-Type: application/json

{"key": "value"}

### Delete Item
DELETE https://httpbin.org/delete
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(specs))
	}

	if specs[0].Method != "GET" || specs[0].Name != "Get Users" {
		t.Errorf("unexpected first request: method=%s name=%s", specs[0].Method, specs[0].Name)
	}
	if specs[1].Method != "POST" || specs[1].Name != "Create Post" {
		t.Errorf("unexpected second request: method=%s name=%s", specs[1].Method, specs[1].Name)
	}
	if specs[2].Method != "DELETE" || specs[2].Name != "Delete Item" {
		t.Errorf("unexpected third request: method=%s name=%s", specs[2].Method, specs[2].Name)
	}
}

func TestParse_RequestsMatchSampleHTTPFile(t *testing.T) {
	// Mirrors the real request.http shipped with the project.
	content := `### Get Users
GET https://httpbin.org/get
Accept: application/json
X-Request-ID: gg-001
X-Client-Name: gg/1.0

### Get Post by Query Param
GET https://httpbin.org/get?userId=1
Accept: application/json
X-Request-ID: gg-002
X-Client-Name: gg/1.0
Cache-Control: no-cache

### Create Post
POST https://httpbin.org/post
Content-Type: application/json
Accept: application/json
X-Request-ID: gg-003
X-Client-Name: gg/1.0

{
  "title": "Gopher Glide http engine",
  "body": "Testing POST requests via gg HTTP engine",
  "userId": 1
}
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(specs))
	}

	// First request
	if specs[0].URL != "https://httpbin.org/get" {
		t.Errorf("request[0] URL mismatch: %s", specs[0].URL)
	}
	// Second request — URL with query param
	if specs[1].URL != "https://httpbin.org/get?userId=1" {
		t.Errorf("request[1] URL mismatch: %s", specs[1].URL)
	}
	if specs[1].Headers.Get("Cache-Control") != "no-cache" {
		t.Errorf("request[1] missing Cache-Control header")
	}
	// Third request — POST with multi-line body
	if specs[2].Method != "POST" {
		t.Errorf("request[2] method mismatch: %s", specs[2].Method)
	}
	if specs[2].Body == "" {
		t.Error("request[2] expected non-empty body")
	}
}

// ── Parse — HTTP methods ──────────────────────────────────────────────────────

func TestParse_AllHTTPMethods(t *testing.T) {
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}

	for _, method := range methods {
		content := "### " + method + " test\n" + method + " https://example.com\n"
		specs, err := Parse(content)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", method, err)
		}
		if len(specs) != 1 {
			t.Fatalf("%s: expected 1 request, got %d", method, len(specs))
		}
		if specs[0].Method != method {
			t.Errorf("%s: expected method=%s, got %s", method, method, specs[0].Method)
		}
	}
}

func TestParse_LowercaseMethodNormalisedToUpper(t *testing.T) {
	content := "### lowercase\nget https://example.com\n"
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specs[0].Method != "GET" {
		t.Errorf("expected GET, got %s", specs[0].Method)
	}
}

// ── Parse — implicit first request (no ### separator) ─────────────────────────

func TestParse_ImplicitFirstRequest(t *testing.T) {
	content := `GET https://example.com/implicit
Accept: text/plain
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(specs))
	}
	if specs[0].Method != "GET" || specs[0].URL != "https://example.com/implicit" {
		t.Errorf("unexpected spec: %+v", specs[0])
	}
}

// ── Parse — comments & whitespace ─────────────────────────────────────────────

func TestParse_HashCommentsIgnored(t *testing.T) {
	content := `# This is a top-level comment

### Named request
# inline comment
GET https://example.com
Accept: application/json
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(specs))
	}
	if specs[0].Method != "GET" {
		t.Errorf("expected GET, got %s", specs[0].Method)
	}
}

func TestParse_SlashCommentsIgnored(t *testing.T) {
	content := `// top level double-slash comment

### Named request
// another comment
GET https://example.com
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(specs))
	}
}

func TestParse_EmptyContent(t *testing.T) {
	specs, err := Parse("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("expected 0 requests, got %d", len(specs))
	}
}

func TestParse_OnlyWhitespaceAndComments(t *testing.T) {
	content := `# just a comment
// another comment

   
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("expected 0 requests, got %d", len(specs))
	}
}

// ── Parse — body trimming ─────────────────────────────────────────────────────

func TestParse_BodyIsTrimmed(t *testing.T) {
	content := `### Body trim test
POST https://example.com

  {"key": "value"}  

`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specs[0].Body != `{"key": "value"}` {
		t.Errorf("expected trimmed body, got %q", specs[0].Body)
	}
}

func TestParse_MultilineBody(t *testing.T) {
	content := `### Multiline body
POST https://example.com
Content-Type: application/json

{
  "a": 1,
  "b": 2
}
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := specs[0].Body
	if body == "" {
		t.Fatal("expected non-empty body")
	}
	if body != "{\n  \"a\": 1,\n  \"b\": 2\n}" {
		t.Errorf("unexpected multiline body: %q", body)
	}
}

// ── Parse — separator / name extraction ──────────────────────────────────────

func TestParse_SeparatorWithNoName(t *testing.T) {
	content := `###
GET https://example.com
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(specs))
	}
	if specs[0].Name != "" {
		t.Errorf("expected empty name, got %q", specs[0].Name)
	}
}

func TestParse_SeparatorNameIsTrimmed(t *testing.T) {
	content := `###   Padded Name   
GET https://example.com
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specs[0].Name != "Padded Name" {
		t.Errorf("expected trimmed name=%q, got %q", "Padded Name", specs[0].Name)
	}
}

// ── Parse — header edge cases ────────────────────────────────────────────────

func TestParse_HeaderWithColonInValue(t *testing.T) {
	content := `### Header colon value
GET https://example.com
Authorization: Bearer token:with:colons
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specs[0].Headers.Get("Authorization") != "Bearer token:with:colons" {
		t.Errorf("unexpected Authorization header: %s", specs[0].Headers.Get("Authorization"))
	}
}

func TestParse_MultipleHeaderValues(t *testing.T) {
	content := `### Multi header
GET https://example.com
X-Custom: value1
X-Custom: value2
`
	specs, err := Parse(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	vals := specs[0].Headers["X-Custom"]
	if len(vals) != 2 {
		t.Fatalf("expected 2 values for X-Custom, got %d", len(vals))
	}
}
