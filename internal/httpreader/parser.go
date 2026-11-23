package httpreader

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// ParseFile reads a .http file and groups it into Journeys via GroupJourneys:
// chained @gg-export → {{var}} requests form multi-step Journeys, the rest stay
// single-step.
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
	specs, _, err := ParseWithSourceMap(content)
	return specs, err
}

// SourceMap locates what Parse accepted, and what it silently dropped, by
// 1-based line number. Requests is parallel to the returned specs.
type SourceMap struct {
	Requests []RequestLines
	// EmptyBlocks are `###` lines whose block never had a request line.
	EmptyBlocks []int
}

// RequestLines is the location data for one request.
type RequestLines struct {
	// Request is the line of the request line (method and URL).
	Request int
	// Exports is parallel to RequestSpec.Exports.
	Exports []int
	// BadExports are `# @gg-export` lines that did not parse and were ignored.
	BadExports []int
	// BadHeaders are header-section lines that are not a valid `Name: value`.
	BadHeaders []int
}

// ParseWithSourceMap is Parse plus a SourceMap for diagnostics.
func ParseWithSourceMap(content string) ([]RequestSpec, *SourceMap, error) {
	var requests []RequestSpec
	var currentRequest *RequestSpec
	sm := &SourceMap{}
	var cur RequestLines
	lineNo := 0

	scanner := bufio.NewScanner(strings.NewReader(content))

	state := 0 // see state legend above

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// JetBrains wraps response scripts in  > {% … %}.
		// Once we enter this block, ignore everything until the closing %}.
		if state == 3 {
			if strings.Contains(trimmed, "%}") {
				state = 2 // back to post-body; further lines may still hold @gg-export
			}
			continue
		}

		if strings.HasPrefix(trimmed, "###") {
			if currentRequest != nil {
				currentRequest.Body = strings.TrimSpace(currentRequest.Body)
				if currentRequest.URL != "" {
					requests = append(requests, *currentRequest)
					sm.Requests = append(sm.Requests, cur)
				} else {
					sm.EmptyBlocks = append(sm.EmptyBlocks, cur.Request)
				}
			}
			currentRequest = &RequestSpec{
				Name:    strings.TrimSpace(strings.TrimPrefix(trimmed, "###")),
				Headers: make(http.Header),
			}
			cur = RequestLines{Request: lineNo}
			state = 0
			continue
		}

		if currentRequest == nil {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			// Implicit start (no leading ### separator)
			currentRequest = &RequestSpec{
				Headers: make(http.Header),
			}
			cur = RequestLines{Request: lineNo}
			state = 0
		}

		// Recognised in states 1 and 2 (after headers or inside/after body).
		// Must be checked before the general "skip comments" guard below.
		if state == 1 || state == 2 {
			if d, ok := parseExportDirective(trimmed); ok {
				currentRequest.Exports = append(currentRequest.Exports, d)
				cur.Exports = append(cur.Exports, lineNo)
				continue
			}
			if strings.HasPrefix(trimmed, exportPrefix) {
				cur.BadExports = append(cur.BadExports, lineNo)
			}
		}

		// In a body (state 2) "#" and "//" lines are content, not comments.
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
				cur.Request = lineNo
			}

		case 1: // Headers
			if trimmed == "" {
				state = 2 // blank line → body
			} else {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					key := strings.TrimSpace(parts[0])
					val := strings.TrimSpace(parts[1])
					if !validHeaderName(key) {
						cur.BadHeaders = append(cur.BadHeaders, lineNo)
					}
					currentRequest.Headers.Add(key, val)
				} else {
					cur.BadHeaders = append(cur.BadHeaders, lineNo)
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

	if currentRequest != nil {
		if currentRequest.URL != "" {
			currentRequest.Body = strings.TrimSpace(currentRequest.Body)
			requests = append(requests, *currentRequest)
			sm.Requests = append(sm.Requests, cur)
		} else {
			sm.EmptyBlocks = append(sm.EmptyBlocks, cur.Request)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scanner error: %w", err)
	}

	for i := range requests {
		requests[i].Compile()
	}

	return requests, sm, nil
}

const exportPrefix = "# @gg-export"

// validHeaderName reports whether name is a non-empty HTTP token.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// parseExportDirective attempts to parse a `# @gg-export` pragma from line.
// Expected format:  # @gg-export <varName> = <engine>: <pattern>
// Returns the parsed directive and true on success; zero value and false otherwise.
func parseExportDirective(line string) (ExportDirective, bool) {
	if !strings.HasPrefix(line, exportPrefix) {
		return ExportDirective{}, false
	}

	rest := strings.TrimSpace(line[len(exportPrefix):])

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
