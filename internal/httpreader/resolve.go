package httpreader

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
)

// ToHTTPRequest converts the spec into an executable http.Request, substituting variables.
func (r *RequestSpec) ToHTTPRequest(vars map[string]string) (*http.Request, error) {
	url := r.URL
	body := r.Body

	// Simple substitution
	for k, v := range vars {
		placeholder := "{{" + k + "}}"
		url = strings.ReplaceAll(url, placeholder, v)
		body = strings.ReplaceAll(body, placeholder, v)
	}

	var bodyReader *bytes.Buffer
	if body != "" {
		bodyReader = bytes.NewBufferString(body)
	} else {
		bodyReader = bytes.NewBuffer([]byte{})
	}

	req, err := http.NewRequest(r.Method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Headers
	for k, vv := range r.Headers {
		for _, v := range vv {
			// Substitute in headers too
			val := v
			for vk, vv := range vars {
				placeholder := "{{" + vk + "}}"
				val = strings.ReplaceAll(val, placeholder, vv)
			}
			req.Header.Add(k, val)
		}
	}

	return req, nil
}
