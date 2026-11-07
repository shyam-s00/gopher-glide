package mcp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shyam-s00/gopher-glide/internal/dryrun"
)

const (
	maxDryRunSteps      = 50
	maxDryRunDuration   = 2 * time.Minute
	maxStepTimeout      = 60 * time.Second
	maxPreviewBytes     = 2048
	defaultStepTimeoutS = 10
)

// HTTPSource names where a .http document comes from: exactly one field is set.
type HTTPSource struct {
	Content string `json:"content,omitempty" jsonschema:"the .http file text. Give either content or path"`
	Path    string `json:"path,omitempty" jsonschema:"path of a .http file on the machine running gg. Give either content or path"`
}

func (s HTTPSource) load() (string, error) {
	switch {
	case s.Content != "" && s.Path != "":
		return "", errors.New("give the .http file as text or as a path, not both (content / http_content, path / http_path)")
	case s.Content == "" && s.Path == "":
		return "", errors.New("give the .http file as text (content / http_content) or as a path on this machine (path / http_path)")
	case s.Content != "":
		if len(s.Content) > dryrun.MaxFileBytes {
			return "", fmt.Errorf("content is larger than %d bytes", dryrun.MaxFileBytes)
		}
		return s.Content, nil
	}
	info, err := os.Stat(s.Path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", s.Path, fsReason(err))
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", s.Path)
	}
	if info.Size() > dryrun.MaxFileBytes {
		return "", fmt.Errorf("%s is larger than %d bytes", s.Path, dryrun.MaxFileBytes)
	}
	b, err := os.ReadFile(s.Path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", s.Path, fsReason(err))
	}
	return string(b), nil
}

// DryRunInput are the arguments of run_journey_dry_run.
type DryRunInput struct {
	HTTPSource
	Variables        map[string]string `json:"variables,omitempty" jsonschema:"values for {{name}} placeholders that no earlier request exports, e.g. an existing auth token"`
	Journeys         []int             `json:"journeys,omitempty" jsonschema:"1-based journey numbers from validate_http_file to run; default all"`
	StepTimeoutS     float64           `json:"step_timeout_s,omitempty" jsonschema:"per-request timeout in seconds, default 10, max 60"`
	BodyPreviewBytes int               `json:"body_preview_bytes,omitempty" jsonschema:"max response bytes shown per step, default 512, max 2048"`
}

// DryRunOutput is the result of run_journey_dry_run.
type DryRunOutput struct {
	// Validation is set instead of Result when the file has errors; nothing was sent.
	Validation *dryrun.Report `json:"validation,omitempty"`
	Result     *dryrun.Result `json:"result,omitempty"`
	Note       string         `json:"note,omitempty"`
}

func registerAuthorTools(s *sdk.Server, l Limits) {
	yes := true
	sdk.AddTool(s, &sdk.Tool{
		Name: "validate_http_file",
		Description: "Check a gg .http file without sending any request. Reports errors and warnings with line numbers " +
			"(bad URLs, undefined {{variables}}, broken @gg-export extractors, ignored lines) and lists the journeys " +
			"(chains of requests) gg will run. Fix every error before using the file. Read gg://docs/http-syntax first.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, Title: "Validate .http file"},
	}, func(_ context.Context, _ *sdk.CallToolRequest, in HTTPSource) (*sdk.CallToolResult, dryrun.Report, error) {
		content, err := in.load()
		if err != nil {
			return nil, dryrun.Report{}, err
		}
		return nil, dryrun.Validate(content), nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "run_journey_dry_run",
		Description: "Run each journey of a .http file ONCE against the real target and report what happened, so you can fix the file " +
			"before generating load. Each request is really sent, including POST/PUT/DELETE, so do not point it at data you cannot afford to change. " +
			"A failing step ends its journey and later steps are skipped. Output shows status, timing, extracted variables and a short " +
			"response preview; secrets (tokens, passwords, cookies, authorization) are masked. The file is validated first and nothing is sent if it has errors.",
		Annotations: &sdk.ToolAnnotations{DestructiveHint: &yes, OpenWorldHint: &yes, Title: "Dry-run journeys"},
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in DryRunInput) (*sdk.CallToolResult, DryRunOutput, error) {
		out, err := dryRun(ctx, in, l)
		return nil, out, err
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "generate_config",
		Description: "Build a valid gg YAML config from parameters: either a built-in profile (expanded into stages, optionally scaled to a peak RPS " +
			"and duration) or explicit stages, plus optional jitter, time_scale and snapshot settings. Returns the YAML text, or the validation errors; " +
			"it never returns an invalid config. Does not write files: save the yaml where the .http file's directory is.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, Title: "Generate config"},
	}, func(_ context.Context, _ *sdk.CallToolRequest, in ConfigParams) (*sdk.CallToolResult, ConfigResult, error) {
		return nil, generateConfig(in, l), nil
	})
}

func dryRun(ctx context.Context, in DryRunInput, l Limits) (DryRunOutput, error) {
	content, err := in.load()
	if err != nil {
		return DryRunOutput{}, err
	}

	supplied := make([]string, 0, len(in.Variables))
	for k := range in.Variables {
		supplied = append(supplied, k)
	}
	if rep := dryrun.ValidateWithVars(content, supplied); !rep.OK {
		return DryRunOutput{Validation: &rep, Note: "the file has errors; nothing was sent. Fix them with validate_http_file and try again"}, nil
	}

	plan, err := dryrun.NewPlan(content)
	if err != nil {
		return DryRunOutput{}, err
	}
	if plan, err = selectJourneys(plan, in.Journeys); err != nil {
		return DryRunOutput{}, err
	}
	steps := 0
	for _, pj := range plan {
		steps += len(pj.Steps)
	}
	if steps > maxDryRunSteps {
		return DryRunOutput{}, fmt.Errorf("%d requests selected; a dry run is limited to %d, pick fewer journeys with the journeys argument", steps, maxDryRunSteps)
	}
	if err := checkStaticHosts(plan, l, false); err != nil {
		return DryRunOutput{}, err
	}

	timeout := time.Duration(in.StepTimeoutS * float64(time.Second))
	switch {
	case in.StepTimeoutS < 0 || timeout > maxStepTimeout:
		return DryRunOutput{}, fmt.Errorf("step_timeout_s must be between 0 and %d", int(maxStepTimeout.Seconds()))
	case timeout == 0:
		timeout = defaultStepTimeoutS * time.Second
	}
	if in.BodyPreviewBytes < 0 || in.BodyPreviewBytes > maxPreviewBytes {
		return DryRunOutput{}, fmt.Errorf("body_preview_bytes must be between 0 and %d", maxPreviewBytes)
	}

	ctx, cancel := context.WithTimeout(ctx, maxDryRunDuration)
	defer cancel()
	res := dryrun.Run(ctx, plan, dryrun.Options{
		StepTimeout:  timeout,
		Vars:         in.Variables,
		PreviewBytes: in.BodyPreviewBytes,
		CheckURL:     l.CheckURL,
	})
	return DryRunOutput{Result: &res}, nil
}

func selectJourneys(plan dryrun.Plan, pick []int) (dryrun.Plan, error) {
	if len(pick) == 0 {
		return plan, nil
	}
	seen := map[int]bool{}
	var sel []int
	for _, n := range pick {
		if n < 1 || n > len(plan) {
			return nil, fmt.Errorf("journey %d does not exist; the file has %d (numbered from 1)", n, len(plan))
		}
		if !seen[n] {
			seen[n] = true
			sel = append(sel, n)
		}
	}
	sort.Ints(sel)
	out := make(dryrun.Plan, 0, len(sel))
	for _, n := range sel {
		out = append(out, plan[n-1])
	}
	return out, nil
}

// checkStaticHosts refuses the whole call, before any traffic, if a fixed URL
// targets a host outside the allowlist. With strict set, a host that is built
// from a variable is refused too when an allowlist is active, since it cannot
// be verified.
func checkStaticHosts(plan dryrun.Plan, l Limits, strict bool) error {
	var bad []string
	for _, pj := range plan {
		for _, ps := range pj.Steps {
			if strings.Contains(ps.Spec.URL, "{{") {
				if strict && len(l.AllowHosts) > 0 && hostIsDynamic(ps.Spec.URL) {
					bad = append(bad, fmt.Sprintf("line %d: host comes from a variable", ps.Line))
				}
				continue
			}
			u, err := url.Parse(ps.Spec.URL)
			if err != nil {
				continue
			}
			if err := l.CheckURL(u); err != nil {
				bad = append(bad, fmt.Sprintf("line %d: %s", ps.Line, u.Host))
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("target host not allowed by --allow-host %s (%s); nothing was sent",
			strings.Join(l.AllowHosts, ","), strings.Join(bad, ", "))
	}
	return nil
}

// hostIsDynamic reports whether the scheme-and-host part of raw contains a variable.
func hostIsDynamic(raw string) bool {
	if strings.HasPrefix(raw, "{{") {
		return true
	}
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return strings.Contains(rest, "{{")
}

// fsReason drops the path from a filesystem error so messages do not repeat it.
func fsReason(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
