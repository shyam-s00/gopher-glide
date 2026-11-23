package hive

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

func specs(pairs ...string) []httpreader.RequestSpec {
	var out []httpreader.RequestSpec
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, httpreader.RequestSpec{Method: pairs[i], URL: pairs[i+1]})
	}
	return out
}

func TestEndpointMatcher_Label(t *testing.T) {
	m := newEndpointMatcher(specs(
		"GET", "http://api.test/users/{{id}}",
		"GET", "http://api.test/users/me",
		"GET", "http://api.test/users/{{id}}/orders/{{oid}}",
		"POST", "http://api.test/login",
		"GET", "{{base}}/items/{{id}}?expand=1",
		"GET", "http://api.test/files/doc-{{n}}",
		"GET", "http://api.test/search?q=widget",
	))
	tests := []struct {
		name, method, url, want string
	}{
		{"template segment", "GET", "http://api.test/users/123", "GET /users/{{id}}"},
		{"query stripped", "GET", "http://api.test/users/123?x=1", "GET /users/{{id}}"},
		{"static beats template", "GET", "http://api.test/users/me", "GET /users/me"},
		{"two variables", "GET", "http://api.test/users/7/orders/99", "GET /users/{{id}}/orders/{{oid}}"},
		{"static spec", "POST", "http://api.test/login", "POST /login"},
		{"method is part of the key", "GET", "http://api.test/login", "GET /login"},
		{"base-url variable template", "GET", "https://prod.example.com:8443/items/5", "GET /items/{{id}}"},
		{"partially templated segment is a wildcard", "GET", "http://api.test/files/doc-17", "GET /files/doc-{{n}}"},
		{"spec query ignored on static", "GET", "http://api.test/search?q=other", "GET /search"},
		{"unresolved template in url still groups", "GET", "http://api.test/users/{{id}}", "GET /users/{{id}}"},
		{"too few segments falls back", "GET", "http://api.test/users", "GET /users"},
		{"too many segments falls back", "GET", "http://api.test/users/1/extra", "GET /users/1/extra"},
		{"unknown path falls back", "GET", "http://api.test/health", "GET /health"},
		{"method case-insensitive", "get", "http://api.test/users/4", "GET /users/{{id}}"},
		{"root", "GET", "http://api.test", "GET /"},
		{"trailing slash", "GET", "http://api.test/users/4/", "GET /users/{{id}}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.label(tt.method, tt.url); got != tt.want {
				t.Errorf("label(%s %s) = %q, want %q", tt.method, tt.url, got, tt.want)
			}
		})
	}
}

func TestEndpointMatcher_NilFallsBackToPath(t *testing.T) {
	var m *endpointMatcher
	if got := m.label("GET", "http://x/users/9?a=b"); got != "GET /users/9" {
		t.Errorf("got %q", got)
	}
}

func TestEndpointMatcher_MostLiteralPatternWins(t *testing.T) {
	m := newEndpointMatcher(specs(
		"GET", "http://x/{{a}}/{{b}}",
		"GET", "http://x/users/{{b}}",
	))
	if got := m.label("GET", "http://x/users/5"); got != "GET /users/{{b}}" {
		t.Errorf("got %q", got)
	}
	if got := m.label("GET", "http://x/orders/5"); got != "GET /{{a}}/{{b}}" {
		t.Errorf("got %q", got)
	}
}

// Failures on /users/1../users/500 from one templated spec are one endpoint.
func TestRecentErrorMix_TopEndpoints_GroupsBySpecTemplate(t *testing.T) {
	e := New()
	e.epMatch.Store(newEndpointMatcher(specs(
		"GET", "http://api.test/users/{{id}}",
		"POST", "http://api.test/checkout",
		"GET", "http://api.test/health",
	)))
	for i := 1; i <= 500; i++ {
		e.logCall(i, "GET", fmt.Sprintf("http://api.test/users/%d", i), 500, time.Millisecond, ErrHttpError)
	}
	for i := 0; i < 30; i++ {
		e.logCall(i, "POST", "http://api.test/checkout", 503, time.Millisecond, ErrHttpError)
	}
	for i := 0; i < 10; i++ {
		e.logCall(i, "GET", "http://api.test/health", 0, time.Millisecond, errBoom)
	}

	mix, ok := e.RecentErrorMix(10 * time.Second)
	if !ok {
		t.Fatal("no mix")
	}
	if len(mix.TopEndpoints) != 3 {
		t.Fatalf("top endpoints = %+v", mix.TopEndpoints)
	}
	byName := map[string]int{}
	for _, te := range mix.TopEndpoints {
		byName[te.Endpoint] = te.N
	}
	if byName["GET /users/{{id}}"] < 100 {
		t.Errorf("users errors should collapse into one endpoint: %+v", mix.TopEndpoints)
	}
	if first := mix.TopEndpoints[0]; first.Endpoint != "GET /users/{{id}}" || first.Kind != "http_5xx" || first.Code != 500 {
		t.Errorf("first = %+v", first)
	}
	for i := 1; i < len(mix.TopEndpoints); i++ {
		if mix.TopEndpoints[i].N > mix.TopEndpoints[i-1].N {
			t.Errorf("not sorted by count: %+v", mix.TopEndpoints)
		}
	}
	if byName["POST /checkout"] != 30 || byName["GET /health"] != 10 {
		t.Errorf("counts = %v", byName)
	}
}

func TestRecentErrorMix_TopEndpoints_KindsAndDominantCode(t *testing.T) {
	e := New()
	e.epMatch.Store(newEndpointMatcher(specs(
		"GET", "http://x/a", "GET", "http://x/b", "GET", "http://x/c", "GET", "http://x/d", "GET", "http://x/e",
	)))
	for i := 0; i < 5; i++ {
		e.logCall(0, "GET", "http://x/a", 503, time.Millisecond, ErrHttpError)
	}
	for i := 0; i < 2; i++ {
		e.logCall(0, "GET", "http://x/a", 502, time.Millisecond, ErrHttpError)
	}
	for i := 0; i < 4; i++ {
		e.logCall(0, "GET", "http://x/b", 429, time.Millisecond, ErrHttpError)
	}
	for i := 0; i < 3; i++ {
		e.logCall(0, "GET", "http://x/c", 0, time.Millisecond, errBoom)
	}
	for i := 0; i < 2; i++ {
		e.logCall(0, "GET", "http://x/d", 200, time.Millisecond, ErrExtractionFailed)
	}
	e.logCall(0, "GET", "http://x/e", 500, time.Millisecond, ErrHttpError)

	mix, _ := e.RecentErrorMix(10 * time.Second)
	got := map[string][2]any{}
	for _, te := range mix.TopEndpoints {
		got[te.Endpoint] = [2]any{te.Kind, te.Code}
	}
	if len(mix.TopEndpoints) != 3 {
		t.Fatalf("want the top 3 only, got %+v", mix.TopEndpoints)
	}
	if got["GET /a"] != [2]any{"http_5xx", 503} {
		t.Errorf("a = %v", got["GET /a"])
	}
	if got["GET /b"] != [2]any{"http_4xx", 429} {
		t.Errorf("b = %v", got["GET /b"])
	}
	if got["GET /c"] != [2]any{"transport", 0} {
		t.Errorf("c = %v", got["GET /c"])
	}
	if _, has := got["GET /e"]; has {
		t.Error("the least-failing endpoint should be cut")
	}
}

func TestRecentErrorMix_TopEndpoints_ExtractKindHasNoCode(t *testing.T) {
	e := New()
	e.logCall(0, "GET", "http://x/a", 200, time.Millisecond, ErrExtractionFailed)
	mix, _ := e.RecentErrorMix(time.Second)
	if len(mix.TopEndpoints) != 1 || mix.TopEndpoints[0].Kind != "extract" || mix.TopEndpoints[0].Code != 0 {
		t.Errorf("top endpoints = %+v", mix.TopEndpoints)
	}
}

// Without a matcher (or for paths it does not know) nothing is dropped.
func TestRecentErrorMix_TopEndpoints_FallbackKeepsEverything(t *testing.T) {
	e := New()
	e.logCall(0, "GET", "http://x/users/1", 500, time.Millisecond, ErrHttpError)
	e.logCall(1, "GET", "http://x/users/2", 500, time.Millisecond, ErrHttpError)
	mix, _ := e.RecentErrorMix(time.Second)
	total := 0
	for _, te := range mix.TopEndpoints {
		total += te.N
	}
	if len(mix.TopEndpoints) != 2 || total != 2 {
		t.Errorf("fallback should keep resolved paths separate: %+v", mix.TopEndpoints)
	}
}

func TestRecentErrorMix_TopEndpoints_OnlyWindowedErrors(t *testing.T) {
	e := New()
	putError(e, 0, 500, "x", time.Now().Add(-time.Minute))
	e.logCall(1, "GET", "http://x/new", 500, time.Millisecond, ErrHttpError)
	mix, _ := e.RecentErrorMix(5 * time.Second)
	if len(mix.TopEndpoints) != 1 || mix.TopEndpoints[0].Endpoint != "GET /new" {
		t.Errorf("top endpoints = %+v", mix.TopEndpoints)
	}
}

// RunStages builds the matcher from the run's specs.
func TestRunStages_BuildsEndpointMatcher(t *testing.T) {
	srv, _ := newCountingServer(t, 500)
	e := New()
	_ = e.RunStages(context.Background(), singleStageCfg(300*time.Millisecond, 20), specs("GET", srv.URL+"/users/{{id}}"))
	if e.epMatch.Load() == nil {
		t.Error("matcher not built")
	}
}
