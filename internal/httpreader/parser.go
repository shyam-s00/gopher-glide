package httpreader

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// ParseFile reads a .http file and returns a list of RequestSpec
func ParseFile(path string) ([]RequestSpec, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read http file: %w", err)
	}
	return Parse(string(content))
}

// Parse parses the content from the.http file
func Parse(content string) ([]RequestSpec, error) {
	var requests []RequestSpec
	var currentRequest *RequestSpec

	scanner := bufio.NewScanner(strings.NewReader(content))

	// states: 0=init (expecting request line), 1=headers, 2=body
	state := 0

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Check for separator
		if strings.HasPrefix(trimmed, "###") {
			if currentRequest != nil {
				// Finalize previous request
				currentRequest.Body = strings.TrimSpace(currentRequest.Body)
				requests = append(requests, *currentRequest)
			}

			// New Request
			currentRequest = &RequestSpec{
				Name:    strings.TrimSpace(strings.TrimPrefix(trimmed, "###")),
				Headers: make(http.Header),
			}
			state = 0
			continue
		}

		// If no request started yet, comments/whitespace are ignored or start a default request
		if currentRequest == nil {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			// Implicit start of the first request
			currentRequest = &RequestSpec{
				Headers: make(http.Header),
			}
			state = 0
		}

		// Handle comments outside the body
		if state != 2 && (strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//")) {
			continue
		}

		switch state {
		case 0: // Request Line
			if trimmed == "" {
				continue
			}
			parts := strings.Fields(trimmed)
			if len(parts) >= 1 {
				method := strings.ToUpper(parts[0])
				isMethod := false
				switch method {
				case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "CONNECT", "TRACE":
					isMethod = true
				}

				if isMethod && len(parts) >= 2 {
					currentRequest.Method = method
					currentRequest.URL = parts[1]
				} else {
					currentRequest.Method = "GET"
					currentRequest.URL = parts[0]
				}
				state = 1
			}
		case 1: // Headers
			if trimmed == "" {
				state = 2 // Empty line -> body starts
			} else {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					key := strings.TrimSpace(parts[0])
					val := strings.TrimSpace(parts[1])
					currentRequest.Headers.Add(key, val)
				}
			}
			// for now let the body added without any validation.
		case 2: // Body
			currentRequest.Body += line + "\n"
		}
	}

	if currentRequest != nil {
		currentRequest.Body = strings.TrimSpace(currentRequest.Body)
		requests = append(requests, *currentRequest)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanner error: %w", err)
	}

	return requests, nil
}
