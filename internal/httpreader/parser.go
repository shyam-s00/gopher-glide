package httpreader

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// ParseFile reads a .http file, parses it into an ordered list of
// RequestSpecs, and applies Smart Detection (GroupJourneys) to group stateful
// @gg-export → {{var}} chains into Journeys. Requests that neither export nor
// consume a chained variable are returned as their own single-step Journey.
func ParseFile(path string) ([]Journey, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read http file: %w", err)
	}
	specs, err := Parse(string(content))
	if err != nil {
		return nil, err
	}
	return GroupJourneys(specs), nil
}

// Parse parses the content of a .http file into an ordered slice of
// RequestSpec values that together form a single stateful Journey.
//
// State machine:
//
//	0 – init / expecting request line
//	1 – headers
//	2 – body
//	3 – JS-embed block (JetBrains `> {% … %}` response handler; ignored)
func Parse(content string) ([]RequestSpec, error) {
	var requests []RequestSpec
	var currentRequest *RequestSpec

	scanner := bufio.NewScanner(strings.NewReader(content))

	state := 0 // see state legend above

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// ── JS-embed safety (state 3) ───────────────────────────────────────
		// JetBrains wraps response scripts in  > {% … %}.
		// Once we enter this block, ignore everything until the closing %}.
		if state == 3 {
			if strings.Contains(trimmed, "%}") {
				state = 2 // back to post-body; further lines may still hold @gg-export
			}
			continue
		}

		// ── Request separator  ###  ─────────────────────────────────────────
		if strings.HasPrefix(trimmed, "###") {
			if currentRequest != nil {
				currentRequest.Body = strings.TrimSpace(currentRequest.Body)
				if currentRequest.URL != "" {
					requests = append(requests, *currentRequest)
				}
			}
			currentRequest = &RequestSpec{
				Name:    strings.TrimSpace(strings.TrimPrefix(trimmed, "###")),
				Headers: make(http.Header),
			}
			state = 0
			continue
		}

		// ── Before the first request ────────────────────────────────────────
		if currentRequest == nil {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			// Implicit start (no leading ### separator)
			currentRequest = &RequestSpec{
				Headers: make(http.Header),
			}
			state = 0
		}

		// ── @gg-export pragma ───────────────────────────────────────────────
		// Recognised in states 1 and 2 (after headers or inside/after body).
		// Must be checked before the general "skip comments" guard below.
		if state == 1 || state == 2 {
			if d, ok := parseExportDirective(trimmed); ok {
				currentRequest.Exports = append(currentRequest.Exports, d)
				continue
			}
		}

		// ── Skip plain comments outside the body ────────────────────────────
		if state != 2 && (strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//")) {
			continue
		}

		switch state {
		case 0: // Request line
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
				state = 2 // blank line → body
			} else {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					key := strings.TrimSpace(parts[0])
					val := strings.TrimSpace(parts[1])
					currentRequest.Headers.Add(key, val)
				}
			}

		case 2: // Body
			// Detect the start of a JetBrains JS-embed response handler.
			if strings.HasPrefix(trimmed, "> {%") {
				state = 3
				continue
			}
			currentRequest.Body += line + "\n"
		}
	}

	if currentRequest != nil && currentRequest.URL != "" {
		currentRequest.Body = strings.TrimSpace(currentRequest.Body)
		requests = append(requests, *currentRequest)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanner error: %w", err)
	}

	for i := range requests {
		requests[i].Compile()
	}

	return requests, nil
}

// parseExportDirective attempts to parse a `# @gg-export` pragma from line.
// Expected format:  # @gg-export <varName> = <engine>: <pattern>
// Returns the parsed directive and true on success; zero value and false otherwise.
func parseExportDirective(line string) (ExportDirective, bool) {
	const prefix = "# @gg-export"
	if !strings.HasPrefix(line, prefix) {
		return ExportDirective{}, false
	}

	rest := strings.TrimSpace(line[len(prefix):])

	// Split on the first '=' to get varName and "engine: pattern"
	eqIdx := strings.Index(rest, "=")
	if eqIdx < 0 {
		return ExportDirective{}, false
	}
	varName := strings.TrimSpace(rest[:eqIdx])
	remainder := strings.TrimSpace(rest[eqIdx+1:])

	// Split remainder on the first ':' to separate engine from pattern
	colonIdx := strings.Index(remainder, ":")
	if colonIdx < 0 {
		return ExportDirective{}, false
	}
	engine := ExportEngine(strings.TrimSpace(remainder[:colonIdx]))
	pattern := strings.TrimSpace(remainder[colonIdx+1:])

	if varName == "" || pattern == "" {
		return ExportDirective{}, false
	}

	return ExportDirective{
		VarName: varName,
		Engine:  engine,
		Pattern: pattern,
	}, true
}
