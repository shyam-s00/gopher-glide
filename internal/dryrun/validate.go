// Package dryrun checks .http files and runs their journeys once, for authoring tools.
package dryrun

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/shyam-s00/gopher-glide/internal/engine/hive"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// MaxFileBytes bounds the size of a .http document accepted for checking.
const MaxFileBytes = 1 << 20

const (
	maxIssues     = 50
	maxListedStep = 100
)

// Issue is one finding, located by 1-based line (0 when it concerns the whole file).
type Issue struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// StepInfo describes one request as gg will run it.
type StepInfo struct {
	Line    int      `json:"line"`
	Name    string   `json:"name,omitempty"`
	Method  string   `json:"method"`
	URL     string   `json:"url"`
	Exports []string `json:"exports,omitempty"`
	Uses    []string `json:"uses,omitempty"`
}

// JourneyInfo is a group of requests gg runs in order with shared variables.
type JourneyInfo struct {
	Index int        `json:"index"`
	Steps []StepInfo `json:"steps"`
}

// Report is the result of Validate.
type Report struct {
	OK              bool          `json:"ok"`
	Errors          []Issue       `json:"errors"`
	Warnings        []Issue       `json:"warnings"`
	Requests        int           `json:"requests"`
	Journeys        []JourneyInfo `json:"journeys"`
	OmittedIssues   int           `json:"omitted_issues,omitempty"`
	OmittedRequests int           `json:"omitted_requests,omitempty"`
}

var (
	dynamicPlaceholder = regexp.MustCompile(`\{\{\$[^}]*\}\}`)
	knownDynamic       = map[string]bool{"{{$uuid}}": true, "{{$randomInt}}": true, "{{$timestamp}}": true}
	varName            = regexp.MustCompile(`^\w+$`)
)

// Validate parses content the way the engine will and reports everything that
// would make a run misbehave, with line numbers.
func Validate(content string) Report { return ValidateWithVars(content, nil) }

// ValidateWithVars is Validate where the named variables are supplied by the
// caller, so using them without an @gg-export is not an error.
func ValidateWithVars(content string, supplied []string) Report {
	r := Report{Errors: []Issue{}, Warnings: []Issue{}, Journeys: []JourneyInfo{}}
	var errs, warns []Issue

	specs, sm, err := httpreader.ParseWithSourceMap(content)
	if err != nil {
		errs = append(errs, Issue{Message: err.Error()})
		return finish(r, errs, warns)
	}
	for _, line := range sm.EmptyBlocks {
		errs = append(errs, Issue{line, "request block has no request line; expected `METHOD URL`"})
	}
	if len(specs) == 0 && len(errs) == 0 {
		errs = append(errs, Issue{0, "no requests found; each request needs a line like `GET https://host/path`"})
	}
	r.Requests = len(specs)

	exporters := map[string][]int{}
	for i, spec := range specs {
		for _, d := range spec.Exports {
			exporters[d.VarName] = append(exporters[d.VarName], sm.Requests[i].Request)
		}
	}

	for i, spec := range specs {
		e, w := checkSpec(spec, sm.Requests[i])
		errs, warns = append(errs, e...), append(warns, w...)
	}

	used := map[string]bool{}
	idx := 0
	for ji, j := range httpreader.GroupJourneys(specs) {
		info := JourneyInfo{Index: ji + 1}
		avail := map[string]bool{}
		for _, v := range supplied {
			avail[v] = true
		}
		for _, spec := range j.Specs {
			lines := sm.Requests[idx]
			idx++
			uses := httpreader.ReferencedVars(spec)
			for _, v := range uses {
				used[v] = true
				if !avail[v] {
					errs = append(errs, Issue{lines.Request, undefinedMessage(v, lines.Request, exporters[v])})
				}
			}
			step := StepInfo{Line: lines.Request, Name: spec.Name, Method: spec.Method, URL: spec.URL, Uses: uses}
			for _, d := range spec.Exports {
				avail[d.VarName] = true
				step.Exports = append(step.Exports, d.VarName)
			}
			if r.Requests > maxListedStep && idx > maxListedStep {
				r.OmittedRequests++
				continue
			}
			info.Steps = append(info.Steps, step)
		}
		if len(info.Steps) > 0 {
			r.Journeys = append(r.Journeys, info)
		}
	}

	for i, spec := range specs {
		for j, d := range spec.Exports {
			if !used[d.VarName] {
				warns = append(warns, Issue{sm.Requests[i].Exports[j],
					fmt.Sprintf("%q is exported but no request uses {{%s}}", d.VarName, d.VarName)})
			}
		}
	}
	return finish(r, errs, warns)
}

func undefinedMessage(v string, line int, from []int) string {
	msg := fmt.Sprintf("{{%s}} is used but no earlier request in this journey exports it", v)
	earlier, later := 0, 0
	for _, l := range from {
		if l < line && earlier == 0 {
			earlier = l
		}
		if l >= line && later == 0 {
			later = l
		}
	}
	switch {
	case earlier > 0:
		return fmt.Sprintf("%s; line %d exports it, but a request that doesn't use it sits between them and ends the chain — move the exporting and using requests next to each other", msg, earlier)
	case later == line:
		return msg + "; a request cannot use a variable it exports itself — put the use in a following request"
	case later > 0:
		return fmt.Sprintf("%s; line %d exports it, but that request comes after this one — reorder them", msg, later)
	}
	return msg + "; add `# @gg-export " + v + " = jsonpath: $.field` to the request that returns it"
}

func checkSpec(spec httpreader.RequestSpec, lines httpreader.RequestLines) (errs, warns []Issue) {
	at := lines.Request

	if !validTarget(spec.URL) {
		errs = append(errs, Issue{at, fmt.Sprintf("URL %q must be absolute and start with http:// or https://", spec.URL)})
	}

	seen := map[string]bool{}
	scan := func(s string) {
		for _, m := range dynamicPlaceholder.FindAllString(s, -1) {
			if !knownDynamic[m] && !seen[m] {
				seen[m] = true
				errs = append(errs, Issue{at, fmt.Sprintf("unknown dynamic value %s; supported: {{$uuid}}, {{$randomInt}}, {{$timestamp}}", m)})
			}
		}
	}
	scan(spec.URL)
	scan(spec.Body)
	for _, vv := range spec.Headers {
		for _, v := range vv {
			scan(v)
		}
	}

	for _, l := range lines.BadExports {
		errs = append(errs, Issue{l, "malformed @gg-export; expected `# @gg-export <name> = <jsonpath|regex>: <pattern>`"})
	}
	for j, d := range spec.Exports {
		errs = append(errs, checkExport(d, lines.Exports[j])...)
	}
	for _, l := range lines.BadHeaders {
		warns = append(warns, Issue{l, "line is not a valid `Name: value` header and was ignored; a blank line must separate headers from the body"})
	}

	if spec.Body != "" && !strings.Contains(spec.Body, "{{") &&
		strings.Contains(strings.ToLower(spec.Headers.Get("Content-Type")), "json") && !json.Valid([]byte(spec.Body)) {
		warns = append(warns, Issue{at, "body is declared as JSON but is not valid JSON"})
	}
	return errs, warns
}

func checkExport(d httpreader.ExportDirective, line int) []Issue {
	var out []Issue
	if !varName.MatchString(d.VarName) {
		out = append(out, Issue{line, fmt.Sprintf("variable name %q can never be referenced; use letters, digits and underscores", d.VarName)})
	}
	switch d.Engine {
	case httpreader.ExportEngineJSONPath:
		if err := hive.ValidateJSONPath(d.Pattern); err != nil {
			out = append(out, Issue{line, fmt.Sprintf("invalid jsonpath %q: %v", d.Pattern, err)})
		}
	case httpreader.ExportEngineRegex:
		if _, err := regexp.Compile(d.Pattern); err != nil {
			out = append(out, Issue{line, fmt.Sprintf("invalid regex %q: %v", d.Pattern, err)})
		}
	default:
		out = append(out, Issue{line, fmt.Sprintf("unknown extractor engine %q; use jsonpath or regex", d.Engine)})
	}
	return out
}

func finish(r Report, errs, warns []Issue) Report {
	byLine := func(s []Issue) {
		sort.SliceStable(s, func(i, j int) bool { return s[i].Line < s[j].Line })
	}
	byLine(errs)
	byLine(warns)
	r.OK = len(errs) == 0
	r.Errors, r.OmittedIssues = capIssues(errs, r.OmittedIssues)
	r.Warnings, r.OmittedIssues = capIssues(warns, r.OmittedIssues)
	return r
}

func capIssues(s []Issue, omitted int) ([]Issue, int) {
	if len(s) > maxIssues {
		return s[:maxIssues], omitted + len(s) - maxIssues
	}
	if s == nil {
		s = []Issue{}
	}
	return s, omitted
}

// validTarget accepts an absolute http(s) URL. A URL that starts with a
// {{variable}} cannot be judged statically and is accepted.
func validTarget(raw string) bool {
	if strings.HasPrefix(raw, "{{") {
		return true
	}
	lower := strings.ToLower(raw)
	rest := ""
	switch {
	case strings.HasPrefix(lower, "http://"):
		rest = raw[len("http://"):]
	case strings.HasPrefix(lower, "https://"):
		rest = raw[len("https://"):]
	default:
		return false
	}
	if rest == "" || rest[0] == '/' {
		return false
	}
	if strings.Contains(raw, "{{") {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Host != ""
}
