// Benchmark suite for internal/httpreader's per-request templating cost.
//
// resolveDynamic and RequestSpec.ToHTTPRequest run on every actor dispatch
// (not just Journey steps), so even small per-call overhead compounds at
// high RPS. These benchmarks compare the static fast-path (Compile()'d spec,
// no variables) against the dynamic slow-paths: {{$uuid}}/{{$randomInt}}/
// {{$timestamp}} placeholders and {{var}} substitution from actor memory.
//
// Run:
//
//	go test -bench=. -benchmem -benchtime=3s ./internal/httpreader/
package httpreader

import (
	"net/http"
	"testing"
)

// ── resolveDynamic ─────────────────────────────────────────────────────────────

// BenchmarkResolveDynamic_NoPlaceholder measures the short-circuit path taken
// when a string contains no "{{$" — the common case for static specs.
func BenchmarkResolveDynamic_NoPlaceholder(b *testing.B) {
	s := "https://api.example.com/v1/checkout/cart"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resolveDynamic(s)
	}
}

// BenchmarkResolveDynamic_Placeholders measures substitution cost for each
// supported dynamic placeholder, and a mixed case combining all three.
func BenchmarkResolveDynamic_Placeholders(b *testing.B) {
	cases := map[string]string{
		"uuid":      "https://api.example.com/v1/users/{{$uuid}}",
		"randomInt": "https://api.example.com/v1/items?qty={{$randomInt}}",
		"timestamp": "https://api.example.com/v1/events?ts={{$timestamp}}",
		"mixed":     "https://api.example.com/v1/users/{{$uuid}}/events?ts={{$timestamp}}&n={{$randomInt}}",
	}
	for name, s := range cases {
		s := s
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = resolveDynamic(s)
			}
		})
	}
}

// ── ToHTTPRequest ────────────────────────────────────────────────────────────

// BenchmarkToHTTPRequest_StaticFastPath measures the Compile()'d fast path:
// PreparsedURL + PrebuiltHeaders, no variables, no dynamic placeholders.
// This is the common case for non-Journey requests and should be the
// cheapest of all variants.
func BenchmarkToHTTPRequest_StaticFastPath(b *testing.B) {
	spec := &RequestSpec{
		Method: http.MethodGet,
		URL:    "https://api.example.com/v1/checkout/cart",
		Headers: http.Header{
			"Accept": []string{"application/json"},
		},
	}
	spec.Compile()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = spec.ToHTTPRequest(nil)
	}
}

// BenchmarkToHTTPRequest_DynamicPlaceholders measures a spec whose URL and
// body contain {{$uuid}}/{{$timestamp}} placeholders — the slow path taken
// for every request of a templated spec, even outside Journey mode.
func BenchmarkToHTTPRequest_DynamicPlaceholders(b *testing.B) {
	spec := &RequestSpec{
		Method: http.MethodPost,
		URL:    "https://api.example.com/v1/orders/{{$uuid}}",
		Body:   `{"idempotency_key":"{{$uuid}}","ts":{{$timestamp}}}`,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}
	spec.Compile()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = spec.ToHTTPRequest(nil)
	}
}

// BenchmarkToHTTPRequest_JourneyVars measures a Journey step that references
// {{varName}} placeholders populated from ActorMemory — the URL, body, and a
// header all require substitution from the vars map on every call.
func BenchmarkToHTTPRequest_JourneyVars(b *testing.B) {
	spec := &RequestSpec{
		Method: http.MethodGet,
		URL:    "https://api.example.com/v1/orders/{{orderId}}",
		Body:   `{"cart_id":"{{cartId}}"}`,
		Headers: http.Header{
			"Authorization": []string{"Bearer {{token}}"},
		},
	}
	spec.Compile()

	vars := map[string]string{
		"orderId": "ord_1234567890",
		"cartId":  "cart_abcdef123456",
		"token":   "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = spec.ToHTTPRequest(vars)
	}
}
