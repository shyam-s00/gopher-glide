package httpreader

import "net/http"

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
	VarName string
	Engine  ExportEngine
	Pattern string
}

// RequestSpec represents a single request defined in a .http file.
// Exports holds the ordered list of @gg-export directives attached to
// this step; they are evaluated after the response is received.
type RequestSpec struct {
	Name    string
	Method  string
	URL     string
	Headers http.Header
	Body    string
	Exports []ExportDirective
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
