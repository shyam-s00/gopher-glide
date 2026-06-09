package httpreader

import (
	"net/http"
	"testing"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func specGET(url string) RequestSpec {
	return RequestSpec{Method: http.MethodGet, URL: url, Headers: make(http.Header)}
}

func specWithExport(url, varName, pattern string) RequestSpec {
	return RequestSpec{
		Method:  http.MethodPost,
		URL:     url,
		Headers: make(http.Header),
		Exports: []ExportDirective{{VarName: varName, Engine: ExportEngineJSONPath, Pattern: pattern}},
	}
}

// ── GroupJourneys ─────────────────────────────────────────────────────────────

func TestGroupJourneys_AllIndependent_FormOwnSingleStepJourneys(t *testing.T) {
	specs := []RequestSpec{
		specGET("https://api.example.com/users"),
		specGET("https://api.example.com/posts"),
		specGET("https://api.example.com/comments"),
	}

	journeys := GroupJourneys(specs)

	if len(journeys) != 3 {
		t.Fatalf("expected 3 independent journeys, got %d", len(journeys))
	}
	for i, j := range journeys {
		if len(j.Specs) != 1 {
			t.Fatalf("journey %d: expected 1 step, got %d", i, len(j.Specs))
		}
		if j.Specs[0].URL != specs[i].URL {
			t.Fatalf("journey %d: expected URL %q, got %q", i, specs[i].URL, j.Specs[0].URL)
		}
	}
}

func TestGroupJourneys_ExportThenConsumer_FormOneJourney(t *testing.T) {
	login := specWithExport("https://api.example.com/login", "token", "$.data.token")
	useToken := RequestSpec{
		Method:  http.MethodGet,
		URL:     "https://api.example.com/me",
		Headers: http.Header{"Authorization": []string{"Bearer {{token}}"}},
	}

	journeys := GroupJourneys([]RequestSpec{login, useToken})

	if len(journeys) != 1 {
		t.Fatalf("expected 1 chained journey, got %d", len(journeys))
	}
	if len(journeys[0].Specs) != 2 {
		t.Fatalf("expected 2 steps in the chained journey, got %d", len(journeys[0].Specs))
	}
	if journeys[0].Specs[0].URL != login.URL || journeys[0].Specs[1].URL != useToken.URL {
		t.Fatalf("journey steps out of order: %+v", journeys[0].Specs)
	}
}

func TestGroupJourneys_MixedFile_IndependentAndChainedCoexist(t *testing.T) {
	independentBefore := specGET("https://api.example.com/health")
	login := specWithExport("https://api.example.com/login", "token", "$.token")
	useToken := RequestSpec{
		Method: http.MethodGet,
		URL:    "https://api.example.com/me?auth={{token}}",
	}
	independentAfter := specGET("https://api.example.com/status")

	journeys := GroupJourneys([]RequestSpec{independentBefore, login, useToken, independentAfter})

	if len(journeys) != 3 {
		t.Fatalf("expected 3 journeys (independent, chain, independent), got %d", len(journeys))
	}
	if len(journeys[0].Specs) != 1 || journeys[0].Specs[0].URL != independentBefore.URL {
		t.Fatalf("journey 0: expected standalone %q, got %+v", independentBefore.URL, journeys[0])
	}
	if len(journeys[1].Specs) != 2 {
		t.Fatalf("journey 1: expected 2-step chain, got %d steps", len(journeys[1].Specs))
	}
	if journeys[1].Specs[0].URL != login.URL || journeys[1].Specs[1].URL != useToken.URL {
		t.Fatalf("journey 1: steps out of order: %+v", journeys[1].Specs)
	}
	if len(journeys[2].Specs) != 1 || journeys[2].Specs[0].URL != independentAfter.URL {
		t.Fatalf("journey 2: expected standalone %q, got %+v", independentAfter.URL, journeys[2])
	}
}

func TestGroupJourneys_ConsecutiveExports_JoinSameBlock(t *testing.T) {
	login := specWithExport("https://api.example.com/login", "token", "$.token")
	createOrder := specWithExport("https://api.example.com/orders", "order_id", "$.id")
	createOrder.Headers = http.Header{"Authorization": []string{"Bearer {{token}}"}}
	fetchOrder := RequestSpec{
		Method: http.MethodGet,
		URL:    "https://api.example.com/orders/{{order_id}}",
	}

	journeys := GroupJourneys([]RequestSpec{login, createOrder, fetchOrder})

	if len(journeys) != 1 {
		t.Fatalf("expected a single 3-step journey, got %d journeys", len(journeys))
	}
	if len(journeys[0].Specs) != 3 {
		t.Fatalf("expected 3 chained steps, got %d", len(journeys[0].Specs))
	}
}

func TestGroupJourneys_DynamicPlaceholders_DoNotTriggerChaining(t *testing.T) {
	specs := []RequestSpec{
		{Method: http.MethodGet, URL: "https://api.example.com/items/{{$uuid}}", Headers: make(http.Header)},
		{Method: http.MethodGet, URL: "https://api.example.com/events/{{$timestamp}}", Headers: make(http.Header)},
	}

	journeys := GroupJourneys(specs)

	if len(journeys) != 2 {
		t.Fatalf("expected dynamic-placeholder requests to remain independent, got %d journeys", len(journeys))
	}
	for i, j := range journeys {
		if len(j.Specs) != 1 {
			t.Fatalf("journey %d: expected 1-step journey, got %d steps", i, len(j.Specs))
		}
	}
}

func TestGroupJourneys_UnconsumedExport_FormsItsOwnBlock(t *testing.T) {
	exportsButUnused := specWithExport("https://api.example.com/login", "token", "$.token")
	independent := specGET("https://api.example.com/health")

	journeys := GroupJourneys([]RequestSpec{exportsButUnused, independent})

	if len(journeys) != 2 {
		t.Fatalf("expected 2 journeys, got %d", len(journeys))
	}
	if len(journeys[0].Specs) != 1 || journeys[0].Specs[0].URL != exportsButUnused.URL {
		t.Fatalf("journey 0: expected the exporting request alone, got %+v", journeys[0])
	}
	if len(journeys[1].Specs) != 1 || journeys[1].Specs[0].URL != independent.URL {
		t.Fatalf("journey 1: expected the independent request alone, got %+v", journeys[1])
	}
}

func TestGroupJourneys_EmptyInput_ReturnsEmpty(t *testing.T) {
	journeys := GroupJourneys(nil)
	if len(journeys) != 0 {
		t.Fatalf("expected no journeys for empty input, got %d", len(journeys))
	}
}

// ── Flatten ───────────────────────────────────────────────────────────────────

func TestFlatten_RoundTripsToOriginalOrder(t *testing.T) {
	specs := []RequestSpec{
		specGET("https://api.example.com/a"),
		specWithExport("https://api.example.com/b", "x", "$.x"),
		specGET("https://api.example.com/c?x={{x}}"),
		specGET("https://api.example.com/d"),
	}

	journeys := GroupJourneys(specs)
	flat := Flatten(journeys)

	if len(flat) != len(specs) {
		t.Fatalf("expected %d specs after flattening, got %d", len(specs), len(flat))
	}
	for i := range specs {
		if flat[i].URL != specs[i].URL {
			t.Fatalf("flatten[%d]: expected %q, got %q", i, specs[i].URL, flat[i].URL)
		}
	}
}

func TestFlatten_EmptyJourneys_ReturnsEmpty(t *testing.T) {
	flat := Flatten(nil)
	if len(flat) != 0 {
		t.Fatalf("expected empty slice, got %d entries", len(flat))
	}
}

// ── ParseFile + Smart Detection (integration) ────────────────────────────────

func TestParseFile_AppliesSmartDetection_MixedFile(t *testing.T) {
	content := `### Health
GET https://api.example.com/health

### Login
POST https://api.example.com/login
Content-Type: application/json

{"user":"a"}

# @gg-export token = jsonpath: $.token

### Use token
GET https://api.example.com/me
Authorization: Bearer {{token}}

### Status
GET https://api.example.com/status
`
	path := writeHTTPFile(t, content)
	journeys, err := ParseFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(journeys) != 3 {
		t.Fatalf("expected 3 journeys (independent, chain, independent), got %d", len(journeys))
	}
	if len(journeys[0].Specs) != 1 {
		t.Fatalf("journey 0: expected 1-step standalone journey, got %d steps", len(journeys[0].Specs))
	}
	if len(journeys[1].Specs) != 2 {
		t.Fatalf("journey 1: expected the login→me chain to form a 2-step journey, got %d steps", len(journeys[1].Specs))
	}
	if len(journeys[2].Specs) != 1 {
		t.Fatalf("journey 2: expected 1-step standalone journey, got %d steps", len(journeys[2].Specs))
	}
}

func TestParseFile_PurelyStateless_OneJourneyPerRequest(t *testing.T) {
	content := `### Users
GET https://api.example.com/users

### Posts
GET https://api.example.com/posts

### Comments
GET https://api.example.com/comments
`
	path := writeHTTPFile(t, content)
	journeys, err := ParseFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(journeys) != 3 {
		t.Fatalf("expected 1 journey per stateless request, got %d journeys", len(journeys))
	}
	for i, j := range journeys {
		if len(j.Specs) != 1 {
			t.Fatalf("journey %d: expected single-step journey, got %d steps", i, len(j.Specs))
		}
	}
}
