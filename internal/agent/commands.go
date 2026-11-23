package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shyam-s00/gopher-glide/internal/dryrun"
	ggmcp "github.com/shyam-s00/gopher-glide/internal/mcp"
	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
)

// Command is a deterministic slash command that needs no LLM.
type Command struct {
	Name        string
	Description string
	Hint        string
}

// Commands are the slash commands, in the order a client lists them.
var Commands = []Command{
	{"dry-run", "Run each journey of a .http file once and report every step", "file.http"},
	{"find-ceiling", "Ramp traffic until health breaches and report the highest sustainable RPS", "file.http [max_p99=250] [max_error_rate=0.01] [max_rps=N]"},
	{"slo-guard", "Run a profile with the autopilot clamping the rate on an SLO breach", "file.http [profile=load] [peak_rps=N] [duration=2m] [max_p99=250]"},
	{"triage", "Compare the two newest snapshots (or baseline and current) and list regressions", "[baseline] [current]"},
}

// Args are a slash command's arguments: bare words and key=value pairs.
type Args struct {
	Pos   []string
	Named map[string]string
}

// ParseSlash splits "/name a b k=v" into its parts; ok is false for ordinary prompt text.
func ParseSlash(text string) (name string, a Args, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", Args{}, false
	}
	fields := strings.Fields(text[1:])
	if len(fields) == 0 {
		return "", Args{}, false
	}
	a.Named = map[string]string{}
	for _, f := range fields[1:] {
		if k, v, found := strings.Cut(f, "="); found && k != "" {
			a.Named[strings.ToLower(strings.ReplaceAll(k, "-", "_"))] = v
			continue
		}
		a.Pos = append(a.Pos, f)
	}
	return strings.ToLower(fields[0]), a, true
}

// IsCommand reports whether name is one of Commands.
func IsCommand(name string) bool {
	for _, c := range Commands {
		if c.Name == name {
			return true
		}
	}
	return false
}

func (a Args) file() string {
	if v := a.Named["file"]; v != "" {
		return v
	}
	if len(a.Pos) > 0 {
		return a.Pos[0]
	}
	return ""
}

func (a Args) float(key string) (float64, error) {
	v := a.Named[key]
	if v == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("%s must be a non-negative number, got %q", key, v)
	}
	return f, nil
}

func (a Args) int(key string) (int, error) {
	v := a.Named[key]
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative whole number, got %q", key, v)
	}
	return n, nil
}

// RunCommand executes a slash command against the gg tools and returns a Markdown summary.
func RunCommand(ctx context.Context, b *Backend, name string, a Args, sink Sink) (string, error) {
	if sink == nil {
		sink = func(Event) {}
	}
	cs, err := b.Client(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cs.Close() }()
	c := &cmdRun{ctx: ctx, cs: cs, sink: sink}
	switch name {
	case "dry-run":
		return c.dryRun(a)
	case "find-ceiling":
		return c.mission(a, "find-ceiling")
	case "slo-guard":
		return c.mission(a, "slo-guard")
	case "triage":
		return c.triage(a)
	}
	return "", fmt.Errorf("unknown command /%s", name)
}

type cmdRun struct {
	ctx  context.Context
	cs   *sdk.ClientSession
	sink Sink
	n    int
}

// call runs one tool, announcing it and its result so a client can render a card.
func (c *cmdRun) call(ctx context.Context, tool string, args map[string]any, out any) error {
	c.n++
	id := fmt.Sprintf("cmd-%d", c.n)
	c.sink(Event{Kind: KindToolCall, ToolID: id, Tool: tool, Args: args})
	err := callTool(ctx, c.cs, tool, args, out)
	res := map[string]any{}
	if err != nil {
		res["error"] = err.Error()
	} else if out != nil {
		if raw, mErr := json.Marshal(out); mErr == nil {
			_ = json.Unmarshal(raw, &res)
		}
	}
	c.sink(Event{Kind: KindToolResult, ToolID: id, Tool: tool, Result: res})
	return err
}

func (c *cmdRun) plan(steps []string, done int) {
	p := make([]PlanStep, len(steps))
	for i, s := range steps {
		st := "pending"
		switch {
		case i < done:
			st = "completed"
		case i == done:
			st = "in_progress"
		}
		p[i] = PlanStep{Title: s, Status: st}
	}
	c.sink(Event{Kind: KindPlan, Plan: p})
}

func (c *cmdRun) dryRun(a Args) (string, error) {
	file := a.file()
	if file == "" {
		return "", errors.New("usage: /dry-run file.http")
	}
	c.plan([]string{"validate", "dry run"}, 0)
	var out ggmcp.DryRunOutput
	if err := c.call(c.ctx, "run_journey_dry_run", map[string]any{"path": file}, &out); err != nil {
		return "", err
	}
	c.plan([]string{"validate", "dry run"}, 2)
	if out.Validation != nil {
		var b strings.Builder
		fmt.Fprintf(&b, "**%s has errors; nothing was sent.**\n\n", file)
		for _, is := range out.Validation.Errors {
			fmt.Fprintf(&b, "- line %d: %s\n", is.Line, is.Message)
		}
		return b.String(), nil
	}
	return renderDryRun(file, out.Result), nil
}

func renderDryRun(file string, r *dryrun.Result) string {
	var b strings.Builder
	verdict := "all steps passed"
	if !r.OK {
		verdict = fmt.Sprintf("%d failed, %d skipped", r.Failed, r.Skipped)
	}
	fmt.Fprintf(&b, "**Dry run of %s: %s** (%d passed)\n\n", file, verdict, r.Passed)
	for _, j := range r.Journeys {
		fmt.Fprintf(&b, "Journey %d\n", j.Index)
		for _, s := range j.Steps {
			detail := s.Status
			if s.HTTPStatus != 0 {
				detail = fmt.Sprintf("%s %d, %.0f ms", s.Status, s.HTTPStatus, s.DurationMS)
			}
			if s.Error != "" {
				detail += ": " + s.Error
			}
			fmt.Fprintf(&b, "- %s %s: %s\n", s.Method, s.URL, detail)
		}
	}
	return b.String()
}

// mission runs find-ceiling or slo-guard through the built-in autopilot and waits for the end.
func (c *cmdRun) mission(a Args, mode string) (string, error) {
	file := a.file()
	if file == "" {
		return "", fmt.Errorf("usage: /%s file.http", mode)
	}
	ap := map[string]any{"mode": mode}
	for key, field := range map[string]string{"max_p99": "max_p99_ms", "max_error_rate": "max_error_rate"} {
		v, err := a.float(key)
		if err != nil {
			return "", err
		}
		if v > 0 {
			ap[field] = v
		}
	}
	start := map[string]any{"http_path": file, "snap": true, "autopilot": ap}
	if mode == "find-ceiling" {
		for _, k := range []string{"start_rps", "max_rps"} {
			n, err := a.int(k)
			if err != nil {
				return "", err
			}
			if n > 0 {
				ap[k] = n
			}
		}
		if d := a.Named["duration"]; d != "" {
			ap["max_duration"] = d
		}
	} else {
		start["profile"] = firstNonEmpty(a.Named["profile"], "load")
		if n, err := a.int("peak_rps"); err != nil {
			return "", err
		} else if n > 0 {
			start["peak_rps"] = n
		}
		if d := a.Named["duration"]; d != "" {
			start["duration"] = d
		}
	}

	steps := []string{"start", "watch", "report"}
	c.plan(steps, 0)
	var started ggmcp.StartOutput
	if err := c.call(c.ctx, "start_simulation", start, &started); err != nil {
		return "", err
	}
	id := started.SessionID
	stopped := false
	defer func() {
		if !stopped {
			StopRunsVia(c.cs, id)
		}
	}()

	c.plan(steps, 1)
	var tel session.Telemetry
	for {
		if err := c.call(c.ctx, "get_live_telemetry", map[string]any{"session_id": id, "wait_s": 5}, &tel); err != nil {
			return "", err
		}
		if tel.State.Terminal() {
			break
		}
		if err := c.ctx.Err(); err != nil {
			return "", err
		}
	}

	c.plan(steps, 2)
	var final session.Telemetry
	if err := c.call(c.ctx, "stop_simulation", map[string]any{"session_id": id, "report": "md"}, &final); err != nil {
		return "", err
	}
	stopped = true
	c.plan(steps, 3)
	return renderMission(mode, file, final), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func renderMission(mode, file string, t session.Telemetry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**/%s on %s: run %s**\n\n", mode, file, t.State)
	if ap := t.Autopilot; ap != nil {
		switch {
		case mode == "find-ceiling" && ap.CeilingRPS > 0:
			fmt.Fprintf(&b, "- ceiling: **%d RPS** (%s)\n", ap.CeilingRPS, ap.Outcome)
		case ap.Outcome != "":
			fmt.Fprintf(&b, "- autopilot outcome: %s\n", ap.Outcome)
		}
		if ap.Reason != "" {
			fmt.Fprintf(&b, "- reason: %s\n", ap.Reason)
		}
	}
	if f := t.Final; f != nil {
		fmt.Fprintf(&b, "- %d requests, error rate %.2f%%, p50 %.0f ms, p95 %.0f ms, p99 %.0f ms\n",
			f.Total, f.ErrorRate*100, f.P50Ms, f.P95Ms, f.P99Ms)
	}
	if t.Message != "" {
		fmt.Fprintf(&b, "- %s\n", t.Message)
	}
	if t.SnapshotPath != "" {
		fmt.Fprintf(&b, "- snapshot: %s\n", t.SnapshotPath)
	}
	switch {
	case t.ReportPath != "":
		fmt.Fprintf(&b, "- report: %s\n", t.ReportPath)
	case t.ReportError != "":
		fmt.Fprintf(&b, "- report failed: %s\n", t.ReportError)
	}
	return b.String()
}

// StopRunsVia stops a simulation on an already open client session, ignoring errors.
func StopRunsVia(cs *sdk.ClientSession, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = callTool(ctx, cs, "stop_simulation", map[string]any{"session_id": id}, nil)
}

func (c *cmdRun) triage(a Args) (string, error) {
	steps := []string{"find snapshots", "compare", "verdict"}
	c.plan(steps, 0)
	base, cur := "", ""
	if len(a.Pos) >= 2 {
		base, cur = a.Pos[0], a.Pos[1]
	} else {
		base, cur = a.Named["baseline"], a.Named["current"]
	}
	if base == "" || cur == "" {
		var list ggmcp.ListOutput
		if err := c.call(c.ctx, "snap_list", map[string]any{"limit": 2}, &list); err != nil {
			return "", err
		}
		if len(list.Snapshots) < 2 {
			return "Need two snapshots to compare; found " + strconv.Itoa(len(list.Snapshots)) +
				". Run with --snap (or `snap: true`) twice, or pass `/triage <baseline> <current>`.", nil
		}
		cur, base = list.Snapshots[0].Path, list.Snapshots[1].Path
	}

	c.plan(steps, 1)
	var diff ggmcp.DiffOutput
	if err := c.call(c.ctx, "snap_diff", map[string]any{"baseline": base, "current": cur}, &diff); err != nil {
		return "", err
	}
	c.plan(steps, 2)
	var as ggmcp.AssertOutput
	if err := c.call(c.ctx, "snap_assert", map[string]any{"baseline": base, "current": cur}, &as); err != nil {
		return "", err
	}
	c.plan(steps, 3)
	return renderTriage(base, cur, diff, as), nil
}

func renderTriage(base, cur string, d ggmcp.DiffOutput, as ggmcp.AssertOutput) string {
	var b strings.Builder
	verdict := "PASS"
	if !as.Passed {
		verdict = "FAIL"
	}
	fmt.Fprintf(&b, "**%s** — baseline `%s` vs current `%s`: %d regression, %d warn, %d pass\n\n",
		verdict, base, cur, d.Counts.Regression, d.Counts.Warn, d.Counts.Pass)
	for _, v := range as.Violations {
		fmt.Fprintf(&b, "- %s (%s): %s\n", v.EndpointID, v.Verdict, v.Message)
	}
	if as.ViolationsOmitted > 0 {
		fmt.Fprintf(&b, "- … and %d more\n", as.ViolationsOmitted)
	}
	if d.Advisory != "" {
		fmt.Fprintf(&b, "\n%s\n", d.Advisory)
	}
	return b.String()
}
