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
