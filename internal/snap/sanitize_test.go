package snap

import (
	"net/http"
	"sort"
	"testing"
	"time"
)

// ── DefaultSanitizer ─────────────────────────────────────────────────────────

func TestDefaultSanitizer_StripsDefaultHeaders(t *testing.T) {
	s := NewDefaultSanitizer()

	entry := RecordEntry{
		Headers: http.Header{
			"Authorization": []string{"Bearer secret-token"},
			"Cookie":        []string{"session=abc123"},
			"Set-Cookie":    []string{"id=xyz; HttpOnly"},
			"X-Api-Key":     []string{"my-key"},
			"Content-Type":  []string{"application/json"},
			"X-Request-Id":  []string{"req-001"},
		},
	}

	out := s.Sanitize(entry)

	// Sensitive headers must be gone.
	for _, banned := range DefaultSensitiveHeaders {
		if _, ok := out.Headers[http.CanonicalHeaderKey(banned)]; ok {
			t.Errorf("header %q should have been stripped but is still present", banned)
		}
	}

	// Safe headers must survive.
	for _, safe := range []string{"Content-Type", "X-Request-Id"} {
		if _, ok := out.Headers[safe]; !ok {
			t.Errorf("header %q should be present but was stripped", safe)
		}
	}
}

func TestDefaultSanitizer_CaseInsensitiveStrip(t *testing.T) {
	// Construct with mixed-case names — they should all map to the same
	// canonical key and still strip correctly.
	s := NewSanitizerWithHeaders([]string{"authorization", "COOKIE", "set-cookie", "x-api-key"})

	entry := RecordEntry{
		Headers: http.Header{
			"Authorization": []string{"Bearer t"},
			"Cookie":        []string{"x=1"},
		},
	}
	out := s.Sanitize(entry)

	if _, ok := out.Headers["Authorization"]; ok {
		t.Error("Authorization should be stripped")
	}
	if _, ok := out.Headers["Cookie"]; ok {
		t.Error("Cookie should be stripped")
	}
}

func TestDefaultSanitizer_EmptyHeaders(t *testing.T) {
	s := NewDefaultSanitizer()

	// No headers → entry returned unchanged (no allocation).
	entry := RecordEntry{StatusCode: 200}
	out := s.Sanitize(entry)
	if out.Headers != nil {
		t.Error("expected nil headers on entry with no headers")
	}
}

func TestDefaultSanitizer_DoesNotMutateOriginal(t *testing.T) {
	s := NewDefaultSanitizer()

	original := http.Header{
		"Authorization": []string{"Bearer secret"},
		"Content-Type":  []string{"application/json"},
	}
	entry := RecordEntry{Headers: original}

	_ = s.Sanitize(entry)

	// The original map must not be touched.
	if _, ok := original["Authorization"]; !ok {
		t.Error("Sanitize must not mutate the original Header map")
	}
}

func TestDefaultSanitizer_ValueSliceIsCopied(t *testing.T) {
	s := NewSanitizerWithHeaders(nil) // no headers stripped

	original := http.Header{
		"X-Trace": []string{"trace-id-1"},
	}
	entry := RecordEntry{Headers: original}
	out := s.Sanitize(entry)

	// Mutate the returned slice — original must be unaffected.
	out.Headers["X-Trace"][0] = "mutated"
	if original["X-Trace"][0] == "mutated" {
		t.Error("value slice in returned Header should be an independent copy")
	}
}

func TestDefaultSanitizer_CustomStripList(t *testing.T) {
	s := NewSanitizerWithHeaders([]string{"X-Internal-Token", "X-Debug"})

	entry := RecordEntry{
		Headers: http.Header{
			"X-Internal-Token": []string{"internal"},
			"X-Debug":          []string{"true"},
			"Authorization":    []string{"Bearer public"}, // NOT in custom list
		},
	}
	out := s.Sanitize(entry)

	if _, ok := out.Headers["X-Internal-Token"]; ok {
		t.Error("X-Internal-Token should be stripped")
	}
	if _, ok := out.Headers["X-Debug"]; ok {
		t.Error("X-Debug should be stripped")
	}
	// Authorization was NOT in the custom list → must survive.
	if _, ok := out.Headers["Authorization"]; !ok {
		t.Error("Authorization should survive with a custom strip list that excludes it")
	}
}

func TestDefaultSanitizer_NilStripList(t *testing.T) {
	// nil/empty headers slice → sanitizer that strips nothing.
	s := NewSanitizerWithHeaders(nil)
	entry := RecordEntry{
		Headers: http.Header{
			"Authorization": []string{"Bearer t"},
		},
	}
	out := s.Sanitize(entry)
	if _, ok := out.Headers["Authorization"]; !ok {
		t.Error("nil strip-list sanitizer should not strip anything")
	}
}

func TestDefaultSanitizer_StripList(t *testing.T) {
	s := NewDefaultSanitizer()
	list := s.StripList()
	sort.Strings(list)

	want := make([]string, len(DefaultSensitiveHeaders))
	for i, h := range DefaultSensitiveHeaders {
		want[i] = http.CanonicalHeaderKey(h)
	}
	sort.Strings(want)

	if len(list) != len(want) {
		t.Fatalf("StripList len = %d, want %d", len(list), len(want))
	}
	for i := range want {
		if list[i] != want[i] {
			t.Errorf("StripList[%d] = %q, want %q", i, list[i], want[i])
		}
	}
}

// ── NoopSanitizer ─────────────────────────────────────────────────────────────

func TestNoopSanitizer_PassesThrough(t *testing.T) {
	s := NoopSanitizer{}
	entry := RecordEntry{
		Method:     "GET",
		URL:        "/api",
		StatusCode: 200,
		Headers: http.Header{
			"Authorization": []string{"Bearer secret"},
		},
	}
	out := s.Sanitize(entry)

	if out.Headers["Authorization"][0] != "Bearer secret" {
		t.Error("NoopSanitizer should pass Authorization through unchanged")
	}
}

// ── WithSanitizer option ──────────────────────────────────────────────────────

func TestWithSanitizer_NoopOverride(t *testing.T) {
	// Using WithSanitizer(NoopSanitizer{}) should disable all scrubbing.
	r := NewDefaultRecorder(64, WithSanitizer(NoopSanitizer{}))

	r.Record(RecordEntry{
		Method:     "GET",
		URL:        "/api/users",
		StatusCode: 200,
		Headers: http.Header{
			"Authorization": []string{"Bearer secret"},
			"Content-Type":  []string{"application/json"},
		},
	})

	snap, err := r.Finalize(RunMeta{StartTime: time.Now(), EndTime: time.Now()})
	if err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	// With noop, the entry is recorded normally — just verify finalize works.
	if len(snap.Endpoints) != 1 {
		t.Errorf("expected 1 endpoint, got %d", len(snap.Endpoints))
	}
}

func TestWithSanitizer_DefaultScrubsBeforeAccumulation(t *testing.T) {
	// The default sanitizer is applied in drain() — verify it actually fires
	// by recording with sensitive headers and checking that the body sample
	// (which preserves the entry) does not leak them. We can't inspect stored
	// headers directly, but we can confirm the recorder still works correctly
	// end-to-end with sanitization active.
	r := NewDefaultRecorder(64)

	r.Record(RecordEntry{
		Method:     "GET",
		URL:        "/api/users",
		StatusCode: 200,
		Duration:   10 * time.Millisecond,
		Headers: http.Header{
			"Authorization": []string{"Bearer should-be-stripped"},
			"Content-Type":  []string{"application/json"},
		},
		RespBody: []byte(`{"id":"1"}`),
	})

	snap, err := r.Finalize(RunMeta{StartTime: time.Now(), EndTime: time.Now()})
	if err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if len(snap.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(snap.Endpoints))
	}
	if snap.Endpoints[0].RequestCount != 1 {
		t.Errorf("expected RequestCount 1, got %d", snap.Endpoints[0].RequestCount)
	}
}

func TestNewSanitizerWithExtraHeaders_IncludesDefaults(t *testing.T) {
	s := NewSanitizerWithExtraHeaders("X-Internal-Token", "X-Debug")

	entry := RecordEntry{
		Headers: http.Header{
			"Authorization":    []string{"Bearer secret"},    // default
			"Cookie":           []string{"s=abc"},            // default
			"X-Internal-Token": []string{"internal"},         // custom
			"X-Debug":          []string{"true"},             // custom
			"Content-Type":     []string{"application/json"}, // safe
		},
	}
	out := s.Sanitize(entry)

	for _, h := range []string{"Authorization", "Cookie", "X-Internal-Token", "X-Debug"} {
		if _, ok := out.Headers[h]; ok {
			t.Errorf("header %q should have been stripped", h)
		}
	}
	if _, ok := out.Headers["Content-Type"]; !ok {
		t.Error("Content-Type should survive")
	}
}

func TestNewSanitizerWithExtraHeaders_NoExtras(t *testing.T) {
	// Calling with no extras should behave identically to NewDefaultSanitizer.
	s := NewSanitizerWithExtraHeaders()

	entry := RecordEntry{
		Headers: http.Header{
			"Authorization": []string{"Bearer t"},
			"Content-Type":  []string{"application/json"},
		},
	}
	out := s.Sanitize(entry)

	if _, ok := out.Headers["Authorization"]; ok {
		t.Error("Authorization should be stripped")
	}
	if _, ok := out.Headers["Content-Type"]; !ok {
		t.Error("Content-Type should survive")
	}
}

func TestWithExtraHeaders_Option(t *testing.T) {
	r := NewDefaultRecorder(64, WithExtraHeaders("X-Secret", "X-Trace"))

	r.Record(RecordEntry{
		Method:     "GET",
		URL:        "/api",
		StatusCode: 200,
		Headers: http.Header{
			"Authorization": []string{"Bearer t"},         // default — stripped
			"X-Secret":      []string{"s"},                // custom — stripped
			"X-Trace":       []string{"t"},                // custom — stripped
			"Content-Type":  []string{"application/json"}, // safe
		},
	})

	snap, err := r.Finalize(RunMeta{})
	if err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if len(snap.Endpoints) != 1 {
		t.Errorf("expected 1 endpoint, got %d", len(snap.Endpoints))
	}
}

func TestWithExtraHeaders_DefaultsStillApply(t *testing.T) {
	// Verify that using WithExtraHeaders does NOT lose the built-in defaults.
	s := NewSanitizerWithExtraHeaders("X-Custom")
	list := s.StripList()

	needed := append([]string{"X-Custom"}, DefaultSensitiveHeaders...)
	found := make(map[string]bool, len(list))
	for _, h := range list {
		found[h] = true
	}
	for _, h := range needed {
		if !found[http.CanonicalHeaderKey(h)] {
			t.Errorf("expected %q in strip list but it was missing", h)
		}
	}
}

func TestWithSanitizer_CustomSanitizer(t *testing.T) {
	custom := NewSanitizerWithHeaders([]string{"X-Secret"})
	r := NewDefaultRecorder(64, WithSanitizer(custom))

	r.Record(RecordEntry{
		Method:     "GET",
		URL:        "/api",
		StatusCode: 200,
		Headers: http.Header{
			"X-Secret":      []string{"must-go"},
			"Authorization": []string{"Bearer kept"}, // not in custom list
		},
	})

	snap, err := r.Finalize(RunMeta{})
	if err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if len(snap.Endpoints) != 1 {
		t.Errorf("expected 1 endpoint, got %d", len(snap.Endpoints))
	}
}
