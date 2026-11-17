package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagents/loopagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/exitlooptool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/mcptoolset"

	"github.com/shyam-s00/gopher-glide/internal/dryrun"
)

// Mode picks which agent runs.
type Mode string

// The three agents.
const (
	ModeGenerate Mode = "generate"
	ModeRun      Mode = "run"
	ModeTriage   Mode = "triage"
)

// DefaultMaxTurns bounds the author-and-verify loop of ModeGenerate.
const DefaultMaxTurns = 4

// Options configure a Runner.
type Options struct {
	Model   model.LLM
	Backend *Backend
	// OutPath is where ModeGenerate saves the finished .http file.
	OutPath string
	// MaxTurns bounds ModeGenerate; default DefaultMaxTurns.
	MaxTurns uint
	// Stream asks the model for partial text so a client can show it as it arrives.
	Stream bool
}

// Result is what a finished agent run produced.
type Result struct {
	// Text is the agent's final message.
	Text string
	// Saved is the .http file ModeGenerate wrote; empty if it never passed.
	Saved string
}

var toolSets = map[Mode][]string{
	ModeGenerate: {"validate_http_file", "run_journey_dry_run"},
	ModeRun: {"validate_http_file", "generate_config", "start_simulation", "get_live_telemetry",
		"steer_traffic", "stop_simulation", "snap_capture", "snap_list"},
	ModeTriage: {"snap_list", "snap_capture", "snap_diff", "snap_assert"},
}

const common = `You drive gg, an HTTP load-testing tool, through its tools. Read the gg://docs/http-syntax resource before writing a .http file. ` +
	`Be concise. Never invent numbers: quote what the tools returned.`

var instructions = map[Mode]string{
	ModeGenerate: common + `

You write a .http file for the user's API and prove it works.
1. Draft the file from the user's description, spec or route list. Chain requests with @gg-export when a later call needs a value from an earlier response.
2. Call validate_http_file and fix every error.
3. Call run_journey_dry_run. If any step is not 2xx, fix the file using the status and body preview.
4. When every step of every journey succeeded, call save_http_file with the full file, then call exit_loop.
5. If something still fails, end your turn with one short line saying what failed and what you will change; you get another turn.
The dry run sends real requests; do not aim it at data that must not change.`,

	ModeRun: common + `

You run a load-test mission from the user's goal and report on it.
1. Validate the .http file. Pick the schedule: for "how much can it take" use start_simulation with autopilot {mode: "find-ceiling"} and no profile; ` +
		`for "hold an SLO under this traffic shape" use a profile with autopilot {mode: "slo-guard"}; otherwise a profile or generate_config. Always set snap: true.
2. Watch with get_live_telemetry using wait_s (about 5) and wake_on_p99_ms / wake_on_error_rate. Judge health on the window figures, not the cumulative ones. ` +
		`Use steer_traffic only when the built-in autopilot is not running; mark moments that explain what you did and why.
3. When the run ends, or the goal is met, call stop_simulation with report "md" if it is still running, then summarise: the goal, what was run, the headline numbers, ` +
		`what degraded first (error_mix.top_endpoints), the autopilot outcome and ceiling if any, and the report_path.
Never leave a simulation running when you finish.`,

	ModeTriage: common + `

You are a forensic analyst for saved snapshots. Use snap_list to find them (newest first), snap_diff to compare a baseline and a current snapshot and snap_assert for the verdict. ` +
		`Correlate latency or error changes with the timeline marks and steering that snap_diff reports and with the per-endpoint error mix; say when the comparison is confounded by steering. ` +
		`End with a ranked root-cause list: endpoint, what changed, by how much, and the most likely reason.`,
}

// Run executes one agent to completion, reporting progress to sink (may be nil).
func Run(ctx context.Context, o Options, mode Mode, task string, sink Sink) (Result, error) {
	if o.Model == nil || o.Backend == nil {
		return Result{}, errors.New("agent needs a model and a backend")
	}
	if strings.TrimSpace(task) == "" {
		return Result{}, errors.New("empty task")
	}
	if sink == nil {
		sink = func(Event) {}
	}

	runs := newRunTracker(o.Backend)
	defer runs.cleanup()

	var saved savedFile
	root, err := buildAgent(ctx, o, mode, &saved)
	if err != nil {
		return Result{}, err
	}
	r, err := runner.NewInMemory("gg-autopilot", root)
	if err != nil {
		return Result{}, err
	}

	cfg := agent.RunConfig{StreamingMode: agent.StreamingModeNone}
	if o.Stream {
		cfg.StreamingMode = agent.StreamingModeSSE
	}
	msg := genai.NewContentFromText(task, genai.RoleUser)
	var text strings.Builder
	conv := &converter{sink: sink}
	for ev, err := range r.Run(ctx, "gg", "session", msg, cfg) {
		if err != nil {
			if ctx.Err() != nil {
				return Result{Text: text.String(), Saved: saved.get()}, ctx.Err()
			}
			return Result{Text: text.String(), Saved: saved.get()}, err
		}
		runs.observe(ev)
		if final := conv.convert(ev); final != "" {
			text.Reset()
			text.WriteString(final)
		}
	}
	res := Result{Text: strings.TrimSpace(text.String()), Saved: saved.get()}
	if mode == ModeGenerate && res.Saved == "" {
		return res, fmt.Errorf("no verified .http file after %d turns: %s", turns(o), lastLine(res.Text))
	}
	return res, nil
}

func turns(o Options) uint {
	if o.MaxTurns == 0 {
		return DefaultMaxTurns
	}
	return o.MaxTurns
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return "the agent gave no explanation"
	}
	return s
}

func buildAgent(ctx context.Context, o Options, mode Mode, saved *savedFile) (agent.Agent, error) {
	names, ok := toolSets[mode]
	if !ok {
		return nil, fmt.Errorf("unknown agent mode %q", mode)
	}
	transport, err := o.Backend.Transport(ctx)
	if err != nil {
		return nil, err
	}
	ts, err := mcptoolset.New(mcptoolset.Config{Transport: transport, ToolFilter: tool.AllowedToolsPredicate(names)})
	if err != nil {
		return nil, fmt.Errorf("gg tools: %w", err)
	}

	cfg := llmagent.Config{
		Name:        "gg_" + string(mode),
		Model:       o.Model,
		Description: "gg " + string(mode) + " agent",
		Instruction: instructions[mode],
		Toolsets:    []tool.Toolset{ts},
	}
	if mode != ModeGenerate {
		return llmagent.New(cfg)
	}

	save, err := saveTool(o.OutPath, saved)
	if err != nil {
		return nil, err
	}
	exit, err := exitlooptool.New()
	if err != nil {
		return nil, err
	}
	cfg.Tools = []tool.Tool{save, exit}
	author, err := llmagent.New(cfg)
	if err != nil {
		return nil, err
	}
	return loopagent.New(loopagent.Config{
		MaxIterations: turns(o),
		AgentConfig:   agent.Config{Name: "gg_generate_loop", SubAgents: []agent.Agent{author}},
	})
}

type savedFile struct {
	mu   sync.Mutex
	path string
}

func (s *savedFile) set(p string) { s.mu.Lock(); s.path = p; s.mu.Unlock() }
func (s *savedFile) get() string  { s.mu.Lock(); defer s.mu.Unlock(); return s.path }

type saveArgs struct {
	Content string `json:"content" jsonschema:"the complete .http file"`
}

type saveResult struct {
	Saved string `json:"saved,omitempty"`
	Error string `json:"error,omitempty"`
}

// saveTool writes the verified file to out, the only path an agent may write.
func saveTool(out string, saved *savedFile) (tool.Tool, error) {
	if out == "" {
		return nil, errors.New("generate needs an output path")
	}
	return functiontool.New(functiontool.Config{
		Name:        "save_http_file",
		Description: "Save the finished .http file once every dry-run step succeeded. Refuses a file that fails validation.",
	}, func(_ agent.Context, in saveArgs) (saveResult, error) {
		if rep := dryrun.Validate(in.Content); !rep.OK {
			return saveResult{Error: "the file still has validation errors; fix them with validate_http_file first"}, nil
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return saveResult{Error: err.Error()}, nil
		}
		if err := os.WriteFile(out, []byte(in.Content), 0o644); err != nil {
			return saveResult{Error: err.Error()}, nil
		}
		saved.set(out)
		return saveResult{Saved: out}, nil
	})
}

// converter turns ADK events into Events, without repeating text that partial events already carried.
type converter struct {
	sink       Sink
	sawPartial bool
}

// convert emits the event and returns the final text of a complete model message.
func (c *converter) convert(ev *session.Event) (final string) {
	if ev == nil || ev.Content == nil {
		return ""
	}
	for _, p := range ev.Content.Parts {
		switch {
		case p.FunctionCall != nil:
			c.sink(Event{Kind: KindToolCall, ToolID: p.FunctionCall.ID, Tool: p.FunctionCall.Name, Args: p.FunctionCall.Args})
		case p.FunctionResponse != nil:
			c.sink(Event{Kind: KindToolResult, ToolID: p.FunctionResponse.ID, Tool: p.FunctionResponse.Name, Result: flatten(p.FunctionResponse.Response)})
		case p.Text == "":
		case p.Thought:
			c.sink(Event{Kind: KindThought, Text: p.Text})
		case ev.Partial:
			c.sawPartial = true
			c.sink(Event{Kind: KindText, Text: p.Text})
		default:
			if !c.sawPartial {
				c.sink(Event{Kind: KindText, Text: p.Text})
			}
			final += p.Text
		}
	}
	if !ev.Partial && final != "" {
		c.sawPartial = false
	}
	return final
}
