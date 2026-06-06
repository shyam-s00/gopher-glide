package httpreader

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
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

// ── Dynamic placeholders ──────────────────────────────────────────────────────

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var uuidFindRE = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`)

// newUUID helper — exercised directly via resolveDynamic.
func TestNewUUID_Format(t *testing.T) {
	u := newUUID()
	if !uuidRE.MatchString(u) {
		t.Errorf("UUID %q does not match RFC-4122 v4 pattern", u)
	}
}

func TestNewUUID_Uniqueness(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		u := newUUID()
		if _, dup := seen[u]; dup {
			t.Fatalf("duplicate UUID after %d iterations: %s", i, u)
		}
		seen[u] = struct{}{}
	}
}

func TestResolveDynamic_UUID_InBody(t *testing.T) {
	spec := &RequestSpec{
		Method:  "POST",
		URL:     "https://example.com/api",
		Headers: make(http.Header),
		Body:    `{"id": "{{$uuid}}"}`,
	}

	req, err := spec.ToHTTPRequest(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	buf := make([]byte, 512)
	n, _ := req.Body.Read(buf)
	body := string(buf[:n])

	// Extract the UUID value from the JSON body
	start := strings.Index(body, `"id": "`) + len(`"id": "`)
	end := strings.LastIndex(body, `"`)
	if start <= 0 || end <= start {
		t.Fatalf("could not parse UUID from body: %s", body)
	}
	got := body[start:end]
	if !uuidRE.MatchString(got) {
		t.Errorf("body UUID %q does not match RFC-4122 v4 pattern", got)
	}
}

func TestResolveDynamic_UUID_MultipleOccurrences_AreDistinct(t *testing.T) {
	spec := &RequestSpec{
		Method:  "POST",
		URL:     "https://example.com/api",
		Headers: make(http.Header),
		Body:    `{"a": "{{$uuid}}", "b": "{{$uuid}}"}`,
	}

	req, err := spec.ToHTTPRequest(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	buf := make([]byte, 512)
	n, _ := req.Body.Read(buf)
	body := string(buf[:n])

	uuids := uuidFindRE.FindAllString(body, -1)
	if len(uuids) != 2 {
		t.Fatalf("expected 2 UUIDs in body, got %d: %s", len(uuids), body)
	}
	if uuids[0] == uuids[1] {
		t.Errorf("expected distinct UUIDs, both are %s", uuids[0])
	}
}

func TestResolveDynamic_UUID_InURL(t *testing.T) {
	spec := &RequestSpec{
		Method:  "GET",
		URL:     "https://example.com/items/{{$uuid}}",
		Headers: make(http.Header),
	}

	req, err := spec.ToHTTPRequest(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parts := strings.Split(req.URL.Path, "/")
	last := parts[len(parts)-1]
	if !uuidRE.MatchString(last) {
		t.Errorf("expected UUID in URL path segment, got %q", last)
	}
}

func TestResolveDynamic_UUID_InHeader(t *testing.T) {
	spec := &RequestSpec{
		Method:  "GET",
		URL:     "https://example.com/api",
		Headers: make(http.Header),
	}
	spec.Headers.Set("X-Request-ID", "{{$uuid}}")

	req, err := spec.ToHTTPRequest(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := req.Header.Get("X-Request-ID")
	if !uuidRE.MatchString(got) {
		t.Errorf("header X-Request-ID %q is not a valid UUID v4", got)
	}
}

func TestResolveDynamic_RandomInt_IsInRange(t *testing.T) {
	spec := &RequestSpec{
		Method:  "POST",
		URL:     "https://example.com/api",
		Headers: make(http.Header),
		Body:    `{"n": {{$randomInt}}}`,
	}

	for i := 0; i < 100; i++ {
		req, err := spec.ToHTTPRequest(nil)
		if err != nil {
			t.Fatalf("unexpected error on iteration %d: %v", i, err)
		}

		buf := make([]byte, 256)
		n, _ := req.Body.Read(buf)
		body := string(buf[:n])

		start := strings.Index(body, `"n": `) + len(`"n": `)
		end := strings.Index(body[start:], "}")
		raw := strings.TrimSpace(body[start : start+end])

		val, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("iteration %d: could not parse randomInt %q: %v", i, raw, err)
		}
		if val < 0 || val > 1_000_000 {
			t.Errorf("iteration %d: randomInt %d out of [0, 1000000]", i, val)
		}
	}
}

func TestResolveDynamic_Timestamp_IsRecentMillis(t *testing.T) {
	before := time.Now().UnixMilli()

	spec := &RequestSpec{
		Method:  "POST",
		URL:     "https://example.com/api",
		Headers: make(http.Header),
		Body:    `{"ts": {{$timestamp}}}`,
	}

	req, err := spec.ToHTTPRequest(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	after := time.Now().UnixMilli()

	buf := make([]byte, 256)
	n, _ := req.Body.Read(buf)
	body := string(buf[:n])

	start := strings.Index(body, `"ts": `) + len(`"ts": `)
	end := strings.Index(body[start:], "}")
	raw := strings.TrimSpace(body[start : start+end])

	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("could not parse timestamp %q: %v", raw, err)
	}
	if ts < before || ts > after {
		t.Errorf("timestamp %d not in [%d, %d]", ts, before, after)
	}
}

func TestResolveDynamic_MixedDynamicAndUserVars(t *testing.T) {
	spec := &RequestSpec{
		Method:  "POST",
		URL:     "https://example.com/api",
		Headers: make(http.Header),
		Body:    `{"id": "{{$uuid}}", "user": "{{username}}"}`,
	}
	vars := map[string]string{"username": "gopher"}

	req, err := spec.ToHTTPRequest(vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	buf := make([]byte, 512)
	n, _ := req.Body.Read(buf)
	body := string(buf[:n])

	if !strings.Contains(body, `"user": "gopher"`) {
		t.Errorf("user var not substituted: %s", body)
	}
	// Should have no leftover {{$uuid}} placeholder
	if strings.Contains(body, "{{$uuid}}") {
		t.Errorf("dynamic placeholder not resolved: %s", body)
	}
	// Verify the UUID portion is valid
	uuids := uuidFindRE.FindAllString(body, -1)
	if len(uuids) != 1 {
		t.Errorf("expected exactly 1 UUID in body, got %d: %s", len(uuids), body)
	}
}

func TestResolveDynamic_UnknownDynamicPlaceholderLeftAsIs(t *testing.T) {
	result := resolveDynamic("{{$unknown}}")
	if result != "{{$unknown}}" {
		t.Errorf("expected unknown placeholder to be left as-is, got %q", result)
	}
}

func TestResolveDynamic_NoPlaceholders_Unchanged(t *testing.T) {
	input := `{"key": "value", "num": 42}`
	if got := resolveDynamic(input); got != input {
		t.Errorf("expected unchanged output, got %q", got)
	}
}
