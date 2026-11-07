package dryrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shyam-s00/gopher-glide/internal/engine/hive"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// Step statuses.
const (
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// Defaults for Options fields left at zero.
const (
	DefaultStepTimeout   = 10 * time.Second
	DefaultPreviewBytes  = 512
	DefaultPreviewBudget = 24 << 10
	DefaultMaxBodyBytes  = 2 << 20
	maxRedirects         = 10
)

// Options tunes a dry run.
type Options struct {
	// Client performs the requests; nil builds a default one.
	Client *http.Client
	// StepTimeout bounds each request including its body read.
	StepTimeout time.Duration
	// Vars seed every journey's variables.
	Vars map[string]string
	// PreviewBytes caps each response preview.
	PreviewBytes int
	// PreviewBudget caps the total preview bytes of passed steps; failed steps
	// always get their full preview.
	PreviewBudget int
	// MaxBodyBytes is the largest response body read; larger ones fail the step.
	MaxBodyBytes int64
	// CheckURL vets every request and redirect target before it is sent.
	CheckURL func(*url.URL) error
}

// Plan is the ordered journeys of a .http document with source lines.
type Plan []PlannedJourney

// PlannedJourney is one chain of requests.
type PlannedJourney struct {
	Steps []PlannedStep
}

// PlannedStep is a request and where it was defined.
type PlannedStep struct {
	Spec httpreader.RequestSpec
	Line int
}

// NewPlan parses content and groups it into journeys exactly as the engine does.
func NewPlan(content string) (Plan, error) {
	specs, sm, err := httpreader.ParseWithSourceMap(content)
	if err != nil {
		return nil, err
	}
	var plan Plan
	i := 0
	for _, j := range httpreader.GroupJourneys(specs) {
		var pj PlannedJourney
		for _, spec := range j.Specs {
			pj.Steps = append(pj.Steps, PlannedStep{Spec: spec, Line: sm.Requests[i].Request})
			i++
		}
		plan = append(plan, pj)
	}
	return plan, nil
}

// StepResult is the outcome of one request.
type StepResult struct {
	Line        int               `json:"line"`
	Name        string            `json:"name,omitempty"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Status      string            `json:"status"`
	HTTPStatus  int               `json:"http_status,omitempty"`
	DurationMS  float64           `json:"duration_ms,omitempty"`
	Error       string            `json:"error,omitempty"`
	Extracted   map[string]string `json:"extracted,omitempty"`
	Headers     map[string]string `json:"response_headers,omitempty"`
	BodyBytes   int64             `json:"body_bytes,omitempty"`
	BodyPreview string            `json:"body_preview,omitempty"`
	Truncated   bool              `json:"truncated,omitempty"`
}

// JourneyResult is the outcome of one chain.
type JourneyResult struct {
	Index int          `json:"index"`
	OK    bool         `json:"ok"`
	Steps []StepResult `json:"steps"`
}

// Result is the outcome of a dry run.
type Result struct {
	OK       bool            `json:"ok"`
	Journeys []JourneyResult `json:"journeys"`
	Passed   int             `json:"steps_passed"`
	Failed   int             `json:"steps_failed"`
	Skipped  int             `json:"steps_skipped"`
}

// shownHeaders are the only response headers echoed; the rest, including
// Set-Cookie and Authorization-like ones, are never returned.
var shownHeaders = []string{"Content-Type", "Content-Length", "Location", "Retry-After", "WWW-Authenticate", "X-Request-Id"}

// Run executes each journey once, in order, sequentially. A step fails on a
// transport error, an HTTP status >= 400, or an extractor that finds nothing;
// the rest of that journey is skipped, as the engine would abort it.
func Run(ctx context.Context, plan Plan, opts Options) Result {
	opts = withDefaults(opts)
	res := Result{OK: true, Journeys: make([]JourneyResult, 0, len(plan))}
	budget := opts.PreviewBudget

	for ji, pj := range plan {
		jr := JourneyResult{Index: ji + 1, OK: true}
		vars := make(map[string]string, len(opts.Vars))
		for k, v := range opts.Vars {
			vars[k] = v
		}
		failed := false
		for _, ps := range pj.Steps {
			var sr StepResult
			if failed {
				sr = StepResult{Line: ps.Line, Name: ps.Spec.Name, Method: ps.Spec.Method, URL: ps.Spec.URL, Status: StatusSkipped}
				res.Skipped++
			} else {
				sr = runStep(ctx, ps, vars, opts, &budget)
				if sr.Status == StatusFailed {
					failed = true
					res.Failed++
				} else {
					res.Passed++
				}
			}
			jr.Steps = append(jr.Steps, sr)
		}
		jr.OK = !failed
		res.OK = res.OK && jr.OK
		res.Journeys = append(res.Journeys, jr)
	}
	return res
}

func withDefaults(o Options) Options {
	if o.StepTimeout <= 0 {
		o.StepTimeout = DefaultStepTimeout
	}
	if o.PreviewBytes <= 0 {
		o.PreviewBytes = DefaultPreviewBytes
	}
	if o.PreviewBudget <= 0 {
		o.PreviewBudget = DefaultPreviewBudget
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if o.Client == nil {
		o.Client = &http.Client{}
	} else {
		c := *o.Client
		o.Client = &c
	}
	check := o.CheckURL
	o.Client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("stopped after 10 redirects")
		}
		if check != nil {
			return check(req.URL)
		}
		return nil
	}
	return o
}

func runStep(ctx context.Context, ps PlannedStep, vars map[string]string, opts Options, budget *int) StepResult {
	spec := ps.Spec
	sr := StepResult{Line: ps.Line, Name: spec.Name, Method: spec.Method, URL: spec.URL, Status: StatusFailed}

	for _, v := range httpreader.ReferencedVars(spec) {
		if _, ok := vars[v]; !ok {
			sr.Error = fmt.Sprintf("{{%s}} has no value: no earlier step exported it and it was not supplied as a variable; nothing was sent", v)
			return sr
		}
	}

	req, err := spec.ToHTTPRequest(vars)
	if err != nil {
		sr.Error = err.Error()
		return sr
	}
	sr.URL = redactURL(req.URL)
	if opts.CheckURL != nil {
		if err := opts.CheckURL(req.URL); err != nil {
			sr.Error = err.Error()
			return sr
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", httpreader.UserAgent)
	}

	sctx, cancel := context.WithTimeout(ctx, opts.StepTimeout)
	defer cancel()
	start := time.Now()
	resp, err := opts.Client.Do(req.WithContext(sctx))
	if err != nil {
		sr.DurationMS = ms(time.Since(start))
		sr.Error = describeError(err, sctx.Err())
		return sr
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBodyBytes+1))
	sr.DurationMS = ms(time.Since(start))
	sr.HTTPStatus = resp.StatusCode
	sr.BodyBytes = int64(len(body))
	sr.Headers = pickHeaders(resp.Header)

	failedWith := func(msg string) StepResult {
		sr.Error = msg
		sr.BodyPreview, sr.Truncated = preview(body, resp.Header.Get("Content-Type"), opts.PreviewBytes)
		return sr
	}
	switch {
	case readErr != nil:
		return failedWith("reading response body: " + describeError(readErr, sctx.Err()))
	case int64(len(body)) > opts.MaxBodyBytes:
		body = body[:opts.MaxBodyBytes]
		sr.BodyBytes = opts.MaxBodyBytes
		return failedWith(fmt.Sprintf("response body is larger than %d bytes", opts.MaxBodyBytes))
	case resp.StatusCode >= 400:
		return failedWith(fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode)))
	}

	for _, d := range spec.Exports {
		val, err := hive.Extract(body, d)
		if err != nil {
			return failedWith(fmt.Sprintf("@gg-export %s failed: %v", d.VarName, err))
		}
		vars[d.VarName] = val
		if sr.Extracted == nil {
			sr.Extracted = map[string]string{}
		}
		sr.Extracted[d.VarName] = showValue(d.VarName, val)
	}

	sr.Status = StatusPassed
	limit := opts.PreviewBytes
	if limit > *budget {
		limit = *budget
	}
	sr.BodyPreview, sr.Truncated = preview(body, resp.Header.Get("Content-Type"), limit)
	*budget -= len(sr.BodyPreview)
	return sr
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// describeError keeps URL query secrets out of transport errors.
func describeError(err, ctxErr error) string {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return "timed out"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		u, perr := url.Parse(ue.URL)
		if perr != nil {
			return ue.Err.Error()
		}
		return fmt.Sprintf("%s %s: %v", ue.Op, redactURL(u), ue.Err)
	}
	return err.Error()
}

func pickHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for _, name := range shownHeaders {
		if v := h.Get(name); v != "" {
			out[name] = clip(v, 200)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func showValue(name, val string) string {
	if sensitiveName(name) {
		return fmt.Sprintf("%s (%d chars)", redacted, utf8.RuneCountInString(val))
	}
	return clip(val, 120)
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}
