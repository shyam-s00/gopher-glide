package snap

import "net/http"

// ── Sanitizer interface ───────────────────────────────────────────────────────

// Sanitizer cleans a RecordEntry before it is accumulated by the recorder.
// Implementations must be safe for concurrent use (called from the drain
// goroutine, but only one goroutine ever calls it).
type Sanitizer interface {
	Sanitize(entry RecordEntry) RecordEntry
}

// ── NoopSanitizer ─────────────────────────────────────────────────────────────

// NoopSanitizer passes entries through unchanged.
// Use it in tests when sanitization is not the concern, or to explicitly
// disable scrubbing.
type NoopSanitizer struct{}

func (NoopSanitizer) Sanitize(entry RecordEntry) RecordEntry { return entry }

// ── DefaultSanitizer ─────────────────────────────────────────────────────────

// DefaultSensitiveHeaders is the out-of-the-box set of headers that are
// stripped from every RecordEntry before it is accumulated.
var DefaultSensitiveHeaders = []string{
	"Authorization",
	"Cookie",
	"Set-Cookie",
	"X-Api-Key",
}

// DefaultSanitizer strips a configurable set of HTTP header names from
// RecordEntry.Headers. It never mutates the original http.Header map — it
// always returns a fresh copy with the sensitive headers omitted.
type DefaultSanitizer struct {
	// stripHeaders stores canonical header keys (net/http canonical form).
	// Using a map[string]struct{} gives O(1) lookup per key.
	stripHeaders map[string]struct{}
}

// NewDefaultSanitizer returns a DefaultSanitizer preloaded with
// DefaultSensitiveHeaders.
func NewDefaultSanitizer() *DefaultSanitizer {
	return NewSanitizerWithHeaders(DefaultSensitiveHeaders)
}

// NewSanitizerWithExtraHeaders returns a DefaultSanitizer that strips
// DefaultSensitiveHeaders plus any additional headers the caller provides.
// Use this when you want the built-in defaults and need to add project-specific
// headers on top (e.g. "X-Internal-Token", "X-Debug").
func NewSanitizerWithExtraHeaders(extra ...string) *DefaultSanitizer {
	merged := make([]string, len(DefaultSensitiveHeaders)+len(extra))
	copy(merged, DefaultSensitiveHeaders)
	copy(merged[len(DefaultSensitiveHeaders):], extra)
	return NewSanitizerWithHeaders(merged)
}

// NewSanitizerWithHeaders returns a DefaultSanitizer that strips exactly the
// provided header names, replacing the default list entirely.
// Names are normalised to canonical form so callers can pass them in any case
// ("authorization", "AUTHORIZATION", etc.).
func NewSanitizerWithHeaders(headers []string) *DefaultSanitizer {
	m := make(map[string]struct{}, len(headers))
	for _, h := range headers {
		m[http.CanonicalHeaderKey(h)] = struct{}{}
	}
	return &DefaultSanitizer{stripHeaders: m}
}

// Sanitize returns a copy of entry whose Headers map excludes every header in
// the strip-list. If the entry has no headers the entry is returned unchanged.
func (s *DefaultSanitizer) Sanitize(entry RecordEntry) RecordEntry {
	if len(entry.Headers) == 0 {
		return entry
	}

	cleaned := make(http.Header, len(entry.Headers))
	for k, v := range entry.Headers {
		if _, strip := s.stripHeaders[k]; strip {
			continue
		}
		// Copy the value slice so the caller's original map is untouched.
		vals := make([]string, len(v))
		copy(vals, v)
		cleaned[k] = vals
	}
	entry.Headers = cleaned
	return entry
}

// StripList returns the set of canonical header names this sanitizer removes.
// Primarily useful for testing and introspection.
func (s *DefaultSanitizer) StripList() []string {
	out := make([]string, 0, len(s.stripHeaders))
	for k := range s.stripHeaders {
		out = append(out, k)
	}
	return out
}
