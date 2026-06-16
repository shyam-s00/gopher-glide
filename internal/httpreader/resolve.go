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
	urlStr := r.URL
	bodyStr := r.Body

	if strings.Contains(urlStr, "{{") {
		urlStr = resolveDynamic(urlStr)
		for k, v := range vars {
			urlStr = strings.ReplaceAll(urlStr, "{{"+k+"}}", v)
		}
	}

	if strings.Contains(bodyStr, "{{") {
		bodyStr = resolveDynamic(bodyStr)
		for k, v := range vars {
			bodyStr = strings.ReplaceAll(bodyStr, "{{"+k+"}}", v)
		}
	}

	var bodyReader io.Reader
	var reqBody io.ReadCloser
	if bodyStr != "" {
		bodyReader = strings.NewReader(bodyStr)
		reqBody = io.NopCloser(bodyReader)
	} else {
		bodyReader = http.NoBody
		reqBody = http.NoBody
	}

	var req *http.Request
	if r.PreparsedURL != nil && urlStr == r.URL {
		// Fast-path: Skip url.Parse and http.NewRequest allocation overhead
		req = &http.Request{
			Method:     r.Method,
			URL:        r.PreparsedURL,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Body:       reqBody,
			Host:       r.PreparsedURL.Host,
		}

		// Set GetBody for redirects/retries
		if bodyStr != "" {
			req.ContentLength = int64(len(bodyStr))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader(bodyStr)), nil
			}
		} else {
			req.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
		}
	} else {
		// Slow-path: Dynamic URL
		var err error
		req, err = http.NewRequest(r.Method, urlStr, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
	}

	// Headers
	if r.PrebuiltHeaders != nil {
		// Fast-path: no per-header dynamic resolution needed
		req.Header = r.PrebuiltHeaders.Clone()
	} else {
		req.Header = make(http.Header, len(r.Headers))
		// Slow-path: Dynamic headers
		for k, vv := range r.Headers {
			for _, v := range vv {
				val := v
				if strings.Contains(val, "{{") {
					val = resolveDynamic(val)
					for vk, vv := range vars {
						val = strings.ReplaceAll(val, "{{"+vk+"}}", vv)
					}
				}
				req.Header.Add(k, val)
			}
		}
	}

	return req, nil
}
