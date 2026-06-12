package hive

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// Extract evaluates d.Engine + d.Pattern against body and returns the captured
// string value.
//
//   - ExportEngineJSONPath: body is unmarshalled into a generic map and the
//     dot-notation path (e.g. $.data.token) is traversed recursively.
//     Array indexing is supported via bracket notation (e.g. $.items[0].id).
//   - ExportEngineRegex: the pattern is compiled and the first capture group
//     of the first match is returned. If the pattern has no capture groups
//     the full match is returned.
func Extract(body []byte, d httpreader.ExportDirective) (string, error) {
	switch d.Engine {
	case httpreader.ExportEngineJSONPath:
		return extractJSONPath(body, d.Pattern)
	case httpreader.ExportEngineRegex:
		return extractRegex(body, d.CompiledRegex, d.Pattern)
	default:
		return "", fmt.Errorf("extractor: unknown engine %q", d.Engine)
	}
}

// ── JSONPath ──────────────────────────────────────────────────────────────────

// extractJSONPath unmarshals body as JSON and traverses path.
// path may optionally begin with "$" followed by a "." separator
// (e.g. "$.data.token", "$token", "$.items[0].id", "data.token").
func extractJSONPath(body []byte, path string) (string, error) {
	// Strip the optional root sigil, if present.
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")

	var root interface{}
	if err := json.Unmarshal(body, &root); err != nil {
		return "", fmt.Errorf("extractor: invalid JSON body: %w", err)
	}

	if path == "" {
		return valueToString(root)
	}

	val, err := traversePath(root, path)
	if err != nil {
		return "", err
	}
	return valueToString(val)
}

// traversePath resolves a dot/bracket-notation path against a parsed JSON
// value. Supported syntax after "$." is stripped:
//
//	"token"              – top-level key
//	"data.token"         – nested key
//	"items[0].id"        – object key then array index then key
//	"data.users[2].name" – arbitrarily deep with array indexing
func traversePath(root interface{}, path string) (interface{}, error) {
	segments := splitPath(path)
	current := root

	for _, seg := range segments {
		if current == nil {
			return nil, fmt.Errorf("extractor: path segment %q: parent is null", seg)
		}

		if idx, key, isArr := parseArraySegment(seg); isArr {
			// Navigate into the object first if a key prefix exists.
			if key != "" {
				m, ok := current.(map[string]interface{})
				if !ok {
					return nil, fmt.Errorf("extractor: expected object at key %q, got %T", key, current)
				}
				val, exists := m[key]
				if !exists {
					return nil, fmt.Errorf("extractor: key %q not found", key)
				}
				current = val
			}
			// Then index into the array.
			arr, ok := current.([]interface{})
			if !ok {
				return nil, fmt.Errorf("extractor: expected array for index %d, got %T", idx, current)
			}
			if idx < 0 || idx >= len(arr) {
				return nil, fmt.Errorf("extractor: index %d out of range (len=%d)", idx, len(arr))
			}
			current = arr[idx]
		} else {
			m, ok := current.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("extractor: expected object at %q, got %T", seg, current)
			}
			val, exists := m[seg]
			if !exists {
				return nil, fmt.Errorf("extractor: key %q not found", seg)
			}
			current = val
		}
	}
	return current, nil
}

// splitPath splits a dot-notation path into segments preserving bracket
// notation inside each segment.
//   - "data.token"          → ["data", "token"]
//   - "items[0].id"         → ["items[0]", "id"]
//   - "a.b[1].c"            → ["a", "b[1]", "c"]
func splitPath(path string) []string {
	return strings.Split(path, ".")
}

// parseArraySegment checks whether seg has the form "key[n]" or "[n]".
// It returns (n, key, true) on success or (0, "", false) otherwise.
func parseArraySegment(seg string) (idx int, key string, isArr bool) {
	open := strings.LastIndex(seg, "[")
	close := strings.LastIndex(seg, "]")
	if open < 0 || close != len(seg)-1 || close < open {
		return 0, "", false
	}
	n, err := strconv.Atoi(seg[open+1 : close])
	if err != nil {
		return 0, "", false
	}
	return n, seg[:open], true
}

// valueToString converts a JSON-unmarshalled leaf value to its string
// representation.
//
//   - string  → returned as-is
//   - float64 → integer-valued floats rendered without decimal point
//   - bool    → "true" / "false"
//   - nil     → extraction error (null is not a useful variable value)
//   - object/array → re-marshalled to compact JSON string
func valueToString(v interface{}) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10), nil
		}
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(t), nil
	case nil:
		return "", fmt.Errorf("extractor: value at path is null")
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("extractor: cannot convert %T to string: %w", v, err)
		}
		return string(b), nil
	}
}

// ── Regex ─────────────────────────────────────────────────────────────────────

// extractRegex executes the pre-compiled regex and returns the first capture group of the
// first match found in body. If the pattern contains no capture groups the
// full match is returned instead.
func extractRegex(body []byte, re *regexp.Regexp, pattern string) (string, error) {
	if re == nil {
		var err error
		re, err = regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("extractor: invalid regex %q: %w", pattern, err)
		}
	}

	matches := re.FindSubmatch(body)
	if matches == nil {
		return "", fmt.Errorf("extractor: regex %q found no match", pattern)
	}

	// Prefer the first explicit capture group; fall back to the full match.
	if len(matches) > 1 {
		return string(matches[1]), nil
	}
	return string(matches[0]), nil
}
