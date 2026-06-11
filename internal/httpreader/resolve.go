package httpreader

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// resolveDynamic replaces each occurrence of a dynamic placeholder with a
// freshly-generated value so that multiple occurrences in one field each
// receive a distinct value (e.g. two {{$uuid}} tokens → two different UUIDs).
//
// Supported placeholders:
//
//	{{$uuid}}       – RFC-4122 UUID v4
//	{{$randomInt}}  – random integer in [0, 1 000 000]
//	{{$timestamp}}  – current UNIX epoch in milliseconds
func resolveDynamic(s string) string {
	const open = "{{$"
	if !strings.Contains(s, open) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))

	for len(s) > 0 {
		idx := strings.Index(s, open)
		if idx == -1 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:idx])
		s = s[idx:]

		end := strings.Index(s, "}}")
		if end == -1 {
			// No closing braces — write the rest as-is.
			b.WriteString(s)
			break
		}

		placeholder := s[:end+2] // includes the closing "}}"
		s = s[end+2:]

		switch placeholder {
		case "{{$uuid}}":
			b.WriteString(newUUID())
		case "{{$randomInt}}":
			b.WriteString(strconv.FormatInt(randomInt(1_000_001), 10))
		case "{{$timestamp}}":
			b.WriteString(strconv.FormatInt(time.Now().UnixMilli(), 10))
		default:
			// Unknown dynamic placeholder — leave it untouched.
			b.WriteString(placeholder)
		}
	}

	return b.String()
}

// randomInt returns a cryptographically random integer in [0, max).
func randomInt(max int64) int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(max))
	if err != nil {
		panic(err)
	}
	return n.Int64()
}

// newUUID generates a random RFC-4122 UUID v4 using crypto/rand.
func newUUID() string {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		panic(err)
	}
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:],
	)
}

// ToHTTPRequest converts the spec into an executable http.Request, substituting variables.
func (r *RequestSpec) ToHTTPRequest(vars map[string]string) (*http.Request, error) {
	// Resolve dynamic placeholders first so that user-defined vars can still
	// reference/override anything that isn't a built-in dynamic placeholder.
	url := resolveDynamic(r.URL)
	body := resolveDynamic(r.Body)

	// User-defined variable substitution
	for k, v := range vars {
		placeholder := "{{" + k + "}}"
		url = strings.ReplaceAll(url, placeholder, v)
		body = strings.ReplaceAll(body, placeholder, v)
	}

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = http.NoBody
	}

	req, err := http.NewRequest(r.Method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Headers
	for k, vv := range r.Headers {
		for _, v := range vv {
			val := resolveDynamic(v)
			for vk, vv := range vars {
				placeholder := "{{" + vk + "}}"
				val = strings.ReplaceAll(val, placeholder, vv)
			}
			req.Header.Add(k, val)
		}
	}

	return req, nil
}
