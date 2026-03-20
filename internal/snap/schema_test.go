package snap

import (
	"encoding/json"
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ── extractFields ─────────────────────────────────────────────────────────────

func TestExtractFields_FlatObject(t *testing.T) {
	body := mustJSON(map[string]any{
		"id":     "u-1",
		"email":  "alice@example.com",
		"age":    30,
		"active": true,
	})

	var v any
	_ = json.Unmarshal(body, &v)
	fields := extractFields(v)

	want := map[string]string{
		"id":     "string",
		"email":  "string",
		"age":    "number",
		"active": "boolean",
	}
	for path, typ := range want {
		if fields[path] != typ {
			t.Errorf("fields[%q] = %q, want %q", path, fields[path], typ)
		}
	}
	// root object itself must NOT appear as a path
	if _, ok := fields[""]; ok {
		t.Error("root empty-path entry should not be present")
	}
}

func TestExtractFields_NestedObject(t *testing.T) {
	body := mustJSON(map[string]any{
		"user": map[string]any{
			"name": "Alice",
			"address": map[string]any{
				"city": "NYC",
			},
		},
	})

	var v any
	_ = json.Unmarshal(body, &v)
	fields := extractFields(v)

	checks := map[string]string{
		"user":              "object",
		"user.name":         "string",
		"user.address":      "object",
		"user.address.city": "string",
	}
	for path, typ := range checks {
		if fields[path] != typ {
			t.Errorf("fields[%q] = %q, want %q", path, fields[path], typ)
		}
	}
}

func TestExtractFields_ArrayOfObjects(t *testing.T) {
	body := mustJSON(map[string]any{
		"items": []any{
			map[string]any{"id": 1, "name": "x"},
			map[string]any{"id": 2, "name": "y"},
		},
	})

	var v any
	_ = json.Unmarshal(body, &v)
	fields := extractFields(v)

	if fields["items"] != "array" {
		t.Errorf("fields[items] = %q, want array", fields["items"])
	}
	if fields["items[].id"] != "number" {
		t.Errorf("fields[items[].id] = %q, want number", fields["items[].id"])
	}
	if fields["items[].name"] != "string" {
		t.Errorf("fields[items[].name] = %q, want string", fields["items[].name"])
	}
}

func TestExtractFields_NullValue(t *testing.T) {
	body := []byte(`{"role": null, "name": "Bob"}`)
	var v any
	_ = json.Unmarshal(body, &v)
	fields := extractFields(v)

	if fields["role"] != "null" {
		t.Errorf("fields[role] = %q, want null", fields["role"])
	}
	if fields["name"] != "string" {
		t.Errorf("fields[name] = %q, want string", fields["name"])
	}
}

func TestExtractFields_EmptyArray(t *testing.T) {
	body := []byte(`{"tags": []}`)
	var v any
	_ = json.Unmarshal(body, &v)
	fields := extractFields(v)

	if fields["tags"] != "array" {
		t.Errorf("fields[tags] = %q, want array", fields["tags"])
	}
	// No element paths expected for an empty array.
	for k := range fields {
		if k == "tags[]."+k {
			t.Errorf("unexpected nested array path: %q", k)
		}
	}
}

// ── InferSchema ───────────────────────────────────────────────────────────────

func TestInferSchema_NilOnNoBodies(t *testing.T) {
	if got := InferSchema(nil); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
	if got := InferSchema([][]byte{}); got != nil {
		t.Errorf("expected nil for empty slice, got %+v", got)
	}
}

func TestInferSchema_SkipsMalformedJSON(t *testing.T) {
	bodies := [][]byte{
		[]byte(`not json`),
		[]byte(`{"id": "1"}`),
		[]byte(`{broken`),
	}
	snap := InferSchema(bodies)
	// Only the valid body contributes — 1 valid sample out of 3 attempted.
	if snap == nil {
		t.Fatal("expected non-nil SchemaSnapshot")
	}
	if snap.Fields["id"].Type != "string" {
		t.Errorf("expected id to be string, got %q", snap.Fields["id"].Type)
	}
}

func TestInferSchema_StableField(t *testing.T) {
	// 100 samples all have "id" → STABLE
	bodies := make([][]byte, 100)
	for i := range bodies {
		bodies[i] = mustJSON(map[string]any{"id": "x", "role": "admin"})
	}
	snap := InferSchema(bodies)
	if snap == nil {
		t.Fatal("nil snapshot")
	}
	if snap.Fields["id"].Stability != StabilityStable {
		t.Errorf("id stability = %q, want STABLE", snap.Fields["id"].Stability)
	}
	if snap.Fields["id"].Presence != 1.0 {
		t.Errorf("id presence = %f, want 1.0", snap.Fields["id"].Presence)
	}
}

func TestInferSchema_VolatileField(t *testing.T) {
	// 100 samples: "role" present in 70 of them → VOLATILE
	bodies := make([][]byte, 100)
	for i := range bodies {
		if i < 70 {
			bodies[i] = mustJSON(map[string]any{"id": "x", "role": "admin"})
		} else {
			bodies[i] = mustJSON(map[string]any{"id": "x"})
		}
	}
	snap := InferSchema(bodies)
	if snap == nil {
		t.Fatal("nil snapshot")
	}
	if snap.Fields["role"].Stability != StabilityVolatile {
		t.Errorf("role stability = %q, want VOLATILE", snap.Fields["role"].Stability)
	}
	if snap.Fields["role"].Presence < 0.69 || snap.Fields["role"].Presence > 0.71 {
		t.Errorf("role presence = %f, want ~0.70", snap.Fields["role"].Presence)
	}
}

func TestInferSchema_RareField(t *testing.T) {
	// 100 samples: "debug" present in only 10 → RARE
	bodies := make([][]byte, 100)
	for i := range bodies {
		if i < 10 {
			bodies[i] = mustJSON(map[string]any{"id": "x", "debug": true})
		} else {
			bodies[i] = mustJSON(map[string]any{"id": "x"})
		}
	}
	snap := InferSchema(bodies)
	if snap.Fields["debug"].Stability != StabilityRare {
		t.Errorf("debug stability = %q, want RARE", snap.Fields["debug"].Stability)
	}
}

func TestInferSchema_TypeVoting(t *testing.T) {
	// "score" is a number in 8 samples, string in 2 → dominant type: number
	bodies := make([][]byte, 10)
	for i := range bodies {
		if i < 8 {
			bodies[i] = mustJSON(map[string]any{"score": 42.0})
		} else {
			bodies[i] = mustJSON(map[string]any{"score": "n/a"})
		}
	}
	snap := InferSchema(bodies)
	if snap.Fields["score"].Type != "number" {
		t.Errorf("score type = %q, want number", snap.Fields["score"].Type)
	}
}

func TestInferSchema_IntegrationWithRecorder(t *testing.T) {
	// Verify the full pipeline: Record with bodies → Finalize → Schema populated.
	r := NewDefaultRecorder(256)

	bodies := []map[string]any{
		{"id": "1", "email": "a@example.com", "role": "admin"},
		{"id": "2", "email": "b@example.com"},
		{"id": "3", "email": "c@example.com", "role": "user"},
	}
	for _, b := range bodies {
		r.Record(RecordEntry{
			Method:     "GET",
			URL:        "/api/users",
			StatusCode: 200,
			RespBody:   mustJSON(b),
		})
	}

	snap, err := r.Finalize(RunMeta{})
	if err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if len(snap.Endpoints) == 0 {
		t.Fatal("no endpoints in snapshot")
	}

	ep := snap.Endpoints[0]
	if ep.Schema == nil {
		t.Fatal("expected Schema to be populated, got nil")
	}

	idField, ok := ep.Schema.Fields["id"]
	if !ok {
		t.Fatal("expected 'id' field in schema")
	}
	if idField.Type != "string" {
		t.Errorf("id type = %q, want string", idField.Type)
	}
	if idField.Presence != 1.0 {
		t.Errorf("id presence = %f, want 1.0", idField.Presence)
	}
	if idField.Stability != StabilityStable {
		t.Errorf("id stability = %q, want STABLE", idField.Stability)
	}

	// "role" present in 2 of 3 samples ≈ 0.667 → VOLATILE
	roleField := ep.Schema.Fields["role"]
	if roleField.Stability != StabilityVolatile {
		t.Errorf("role stability = %q, want VOLATILE", roleField.Stability)
	}
}

func TestInferSchema_SpecExample(t *testing.T) {
	// Matches the JSON example from gg-snap.md exactly.
	bodies := make([][]byte, 100)
	for i := range bodies {
		m := map[string]any{
			"id":    "u-1",
			"email": "alice@example.com",
		}
		// "role" present in 82 of 100 samples
		if i < 82 {
			m["role"] = "admin"
		}
		bodies[i] = mustJSON(m)
	}

	snap := InferSchema(bodies)
	if snap == nil {
		t.Fatal("nil snapshot")
	}

	checks := []struct {
		field    string
		wantType string
		wantStab string
		minPres  float64
		maxPres  float64
	}{
		{"id", "string", StabilityStable, 0.99, 1.01},
		{"email", "string", StabilityStable, 0.99, 1.01},
		{"role", "string", StabilityVolatile, 0.81, 0.83},
	}
	for _, c := range checks {
		f, ok := snap.Fields[c.field]
		if !ok {
			t.Errorf("field %q missing from schema", c.field)
			continue
		}
		if f.Type != c.wantType {
			t.Errorf("%s.type = %q, want %q", c.field, f.Type, c.wantType)
		}
		if f.Stability != c.wantStab {
			t.Errorf("%s.stability = %q, want %q", c.field, f.Stability, c.wantStab)
		}
		if f.Presence < c.minPres || f.Presence > c.maxPres {
			t.Errorf("%s.presence = %f, want [%f, %f]", c.field, f.Presence, c.minPres, c.maxPres)
		}
	}
}

// ── stability scorer ──────────────────────────────────────────────────────────

func TestScoreStability_Boundaries(t *testing.T) {
	cases := []struct {
		presence float64
		want     string
	}{
		{1.00, StabilityStable},
		{0.95, StabilityStable},
		{0.94, StabilityVolatile},
		{0.50, StabilityVolatile},
		{0.49, StabilityRare},
		{0.00, StabilityRare},
	}
	for _, c := range cases {
		got := scoreStability(c.presence)
		if got != c.want {
			t.Errorf("scoreStability(%v) = %q, want %q", c.presence, got, c.want)
		}
	}
}

// ── dominant type ─────────────────────────────────────────────────────────────

func TestDominantType_TieBreak(t *testing.T) {
	// Tie between "string" and "null" — string has higher rank → wins.
	votes := map[string]int{"string": 1, "null": 1}
	got := dominantType(votes)
	if got != "string" {
		t.Errorf("dominantType tie: got %q, want string", got)
	}
}

func TestDominantType_ClearWinner(t *testing.T) {
	votes := map[string]int{"number": 7, "string": 2, "null": 1}
	got := dominantType(votes)
	if got != "number" {
		t.Errorf("got %q, want number", got)
	}
}
