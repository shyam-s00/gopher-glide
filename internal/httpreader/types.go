package httpreader

import "net/http"

// RequestSpec represents a single request defined in a .http file
type RequestSpec struct {
	Name    string
	Method  string
	URL     string
	Headers http.Header
	Body    string
}
