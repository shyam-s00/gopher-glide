package httpreader

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// UserAgent is the User-Agent header value gg sends on every request.
const UserAgent = "gg/1.0"

// ExportEngine enumerates the supported variable-extraction strategies.
type ExportEngine string

const (
	ExportEngineJSONPath ExportEngine = "jsonpath"
	ExportEngineRegex    ExportEngine = "regex"
)

// ExportDirective represents a single `# @gg-export` pragma attached to a
// RequestSpec.  After the request is executed the engine will evaluate
// Pattern against the response body using Engine and store the result in
// VarName for use by subsequent requests in the same journey.
type ExportDirective struct {
	VarName       string
	Engine        ExportEngine
	Pattern       string
	CompiledRegex *regexp.Regexp // cached during Compile()
}

// RequestSpec represents a single request defined in a .http file.
// Exports holds the ordered list of @gg-export directives attached to
// this step; they are evaluated after the response is received.
type RequestSpec struct {
	Name            string
	Method          string
	URL             string
	Headers         http.Header
	Body            string
	Exports         []ExportDirective
	PreparsedURL    *url.URL
	ResolvedURL     string // cached PreparsedURL.String(), set during Compile()
	PrebuiltHeaders http.Header
}

// Compile caches optimizations for static fields, heavily reducing memory
// allocations and GC pressure for requests that do not use variables.
func (r *RequestSpec) Compile() {
	if !strings.Contains(r.URL, "{{") {
		if parsed, err := url.Parse(r.URL); err == nil {
			r.PreparsedURL = parsed
			r.ResolvedURL = parsed.String()
		}
	}

	hasVar := false
	for _, vv := range r.Headers {
		for _, v := range vv {
			if strings.Contains(v, "{{") {
				hasVar = true
				break
			}
		}
		if hasVar {
			break
		}
	}

	if !hasVar {
		h := r.Headers.Clone()
		if h == nil {
			h = make(http.Header, 1)
		}
		// Baked in here so the hot path never needs a per-request Header.Set.
		h.Set("User-Agent", UserAgent)
		r.PrebuiltHeaders = h
	}

	for i, d := range r.Exports {
		if d.Engine == ExportEngineRegex && d.CompiledRegex == nil {
			if re, err := regexp.Compile(d.Pattern); err == nil {
				r.Exports[i].CompiledRegex = re
			}
		}
	}
}

// Journey is an ordered, contiguous run of RequestSpecs that an Actor
// executes sequentially as a single stateful unit, sharing one ActorMemory.
//
// Smart Detection (see GroupJourneys) produces these automatically: requests
// linked by a `# @gg-export` → `{{var}}` chain are grouped into a multi-step
// Journey, while every other request becomes its own independent single-step
// Journey — so a purely stateless `.http` file behaves exactly as before.
type Journey struct {
	Specs []RequestSpec
}
