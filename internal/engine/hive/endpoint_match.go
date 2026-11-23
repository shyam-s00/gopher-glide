package hive

import (
	"strings"

	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// endpointMatcher maps a resolved request URL back to the spec that produced it,
// so failures group by "GET /users/{{id}}" instead of one entry per id. It is
// built once per run and used only when a heartbeat summarises errors.
type endpointMatcher struct {
	exact    map[string]string // "METHOD /static/path" -> label
	patterns []pathPattern
}

// pathPattern is a templated spec path; a nil segment matches any one segment.
type pathPattern struct {
	method string
	segs   []string
	label  string
}

func newEndpointMatcher(specs []httpreader.RequestSpec) *endpointMatcher {
	m := &endpointMatcher{exact: make(map[string]string, len(specs))}
	for i := range specs {
		method := strings.ToUpper(specs[i].Method)
		path := urlPath(specs[i].URL)
		label := method + " " + path
		if !strings.Contains(path, "{{") {
			if _, dup := m.exact[label]; !dup {
				m.exact[label] = label
			}
			continue
		}
		parts := strings.Split(strings.Trim(path, "/"), "/")
		for j, p := range parts {
			if strings.Contains(p, "{{") {
				parts[j] = ""
			}
		}
		m.patterns = append(m.patterns, pathPattern{method: method, segs: parts, label: label})
	}
	return m
}

// label returns the group for a failed call: the matching spec's template, or
// "METHOD <resolved path>" when nothing matches.
func (m *endpointMatcher) label(method, rawURL string) string {
	method = strings.ToUpper(method)
	path := urlPath(rawURL)
	key := method + " " + path
	if m == nil {
		return key
	}
	if l, ok := m.exact[key]; ok {
		return l
	}
	best, bestLiteral := "", -1
	for i := range m.patterns {
		p := &m.patterns[i]
		if p.method != method {
			continue
		}
		if lit, ok := p.match(path); ok && lit > bestLiteral {
			best, bestLiteral = p.label, lit
		}
	}
	if bestLiteral >= 0 {
		return best
	}
	return key
}

// match reports whether path fits the pattern and how many segments matched
// literally, so the most specific pattern wins.
func (p *pathPattern) match(path string) (literal int, ok bool) {
	rest := strings.Trim(path, "/")
	more := true
	for _, seg := range p.segs {
		if !more {
			return 0, false
		}
		var cur string
		cur, rest, more = strings.Cut(rest, "/")
		if cur == "" {
			return 0, false
		}
		if seg == "" {
			continue
		}
		if seg != cur {
			return 0, false
		}
		literal++
	}
	return literal, !more
}

// urlPath returns the path of a URL or URL template with scheme, host and query
// removed. A leading {{var}} (e.g. a base URL variable) counts as the host.
func urlPath(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
		if j := strings.IndexByte(raw, '/'); j >= 0 {
			raw = raw[j:]
		} else {
			raw = "/"
		}
	} else if strings.HasPrefix(raw, "{{") {
		if end := strings.Index(raw, "}}"); end >= 0 {
			raw = raw[end+2:]
		}
		if raw == "" {
			raw = "/"
		}
	}
	if raw == "" || raw[0] != '/' {
		raw = "/" + raw
	}
	return raw
}
