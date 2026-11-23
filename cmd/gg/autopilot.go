package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/agent"
	"github.com/shyam-s00/gopher-glide/internal/mcp"
	"github.com/shyam-s00/gopher-glide/internal/version"
)

const mcpURLEnv = "GG_MCP_URL"

// maxSpecBytes caps a spec file inlined into a generate prompt.
const maxSpecBytes = 256 << 10

type autopilotCmd struct {
	acp      bool
	mode     agent.Mode
	task     string
	out      string
	maxTurns uint
	model    agent.ModelConfig
	mcpURL   string
	token    string
	limits   mcp.Limits
	logLevel slog.Level
}

func autopilotUsage(out io.Writer) {
	_, _ = fmt.Fprint(out, `Usage: gg autopilot generate [flags] <what the API does | path to an OpenAPI spec>
       gg autopilot run      [flags] <mission goal>
       gg autopilot triage   [flags] [question]
       gg autopilot --acp    [flags]

LLM agents that drive gg through its MCP tools.
  generate  write a .http file and prove it with a dry run (up to --max-turns attempts)
  run       turn a goal into a monitored simulation and report the result
  triage    compare saved snapshots and name the likely root causes
  --acp     serve the Agent Client Protocol on stdio (JetBrains AI, Zed). Slash commands
            /dry-run /find-ceiling /slo-guard /triage work without an LLM.

Model: Gemini via GEMINI_API_KEY (or Vertex AI), or any OpenAI-compatible server with
--base-url and --model. Environment: GG_AUTOPILOT_MODEL, GG_AUTOPILOT_BASE_URL, GG_AUTOPILOT_API_KEY.

Flags:
`)
}

func parseAutopilotFlags(args []string, out io.Writer) (autopilotCmd, error) {
	def := mcp.DefaultLimits()
	fs := flag.NewFlagSet("gg autopilot", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { autopilotUsage(out); fs.PrintDefaults() }

	acp := fs.Bool("acp", false, "serve the Agent Client Protocol on stdio")
	modelName := fs.String("model", "", "model name (default "+agent.DefaultModel+" for Gemini; required with --base-url)")
	baseURL := fs.String("base-url", "", "OpenAI-compatible chat endpoint, e.g. http://localhost:11434/v1 for Ollama")
	outPath := fs.String("out", "requests.http", "generate: where to save the verified .http file")
	maxTurns := fs.Uint("max-turns", agent.DefaultMaxTurns, "generate: attempts before giving up")
	mcpURL := fs.String("mcp-url", "", "use a running gg mcp daemon at this /mcp URL instead of an in-process server (default $"+mcpURLEnv+")")
	token := fs.String("auth-token", "", "bearer token for --mcp-url (or set "+tokenEnv+")")
	maxRPS := fs.Int("max-rps", def.MaxRPS, "in-process server: highest RPS any simulation may use")
	maxDur := fs.Duration("max-duration", def.MaxDuration, "in-process server: longest simulation")
	var hosts hostList
	fs.Var(&hosts, "allow-host", "in-process server: restrict targets to this host or host:port (repeatable)")
	logLevel := fs.String("log-level", "warn", "log verbosity on stderr: debug, info, warn, error")

	// The mode word comes first; flags may follow it or the task.
	var mode agent.Mode
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		switch m := agent.Mode(rest[0]); m {
		case agent.ModeGenerate, agent.ModeRun, agent.ModeTriage:
			mode, rest = m, rest[1:]
		default:
			return autopilotCmd{}, fmt.Errorf("unknown mode %q (use generate, run, triage or --acp)", rest[0])
		}
	}
	if err := parseInterspersed(fs, rest); err != nil {
		return autopilotCmd{}, err
	}

	c := autopilotCmd{acp: *acp, mode: mode, out: *outPath, maxTurns: *maxTurns, mcpURL: *mcpURL, token: *token,
		model:  agent.ModelConfig{Name: *modelName, BaseURL: *baseURL},
		limits: mcp.Limits{MaxRPS: *maxRPS, MaxDuration: *maxDur, MaxSessions: def.MaxSessions, MaxUserSessions: def.MaxUserSessions, AllowHosts: hosts},
	}
	if err := c.logLevel.UnmarshalText([]byte(*logLevel)); err != nil {
		return autopilotCmd{}, fmt.Errorf("invalid --log-level %q (use debug, info, warn or error)", *logLevel)
	}
	if c.mcpURL == "" {
		c.mcpURL = os.Getenv(mcpURLEnv)
	}
	if c.token == "" {
		c.token = os.Getenv(tokenEnv)
	}
	if err := c.limits.Validate(); err != nil {
		return autopilotCmd{}, err
	}
	if c.maxTurns == 0 {
		return autopilotCmd{}, errors.New("--max-turns must be at least 1")
	}

	positional := fs.Args()
	switch {
	case c.acp && mode != "":
		return autopilotCmd{}, errors.New("--acp cannot be combined with a mode")
	case c.acp && len(positional) > 0:
		return autopilotCmd{}, fmt.Errorf("--acp takes no arguments, got %q", positional[0])
	case !c.acp && mode == "":
		return autopilotCmd{}, errors.New("give a mode (generate, run or triage) or --acp")
	}
	c.task = strings.TrimSpace(strings.Join(positional, " "))
	switch {
	case c.acp:
	case mode == agent.ModeTriage && c.task == "":
		c.task = "Compare the two newest snapshots and explain any regression."
	case c.task == "":
		return autopilotCmd{}, fmt.Errorf("gg autopilot %s needs a task: pass it as an argument", mode)
	}
	return c, nil
}

// parseInterspersed parses flags that may come before, between or after positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) error {
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return fs.Parse(append([]string{"--"}, pos...))
}

func runAutopilotCmd(args []string) {
	c, err := parseAutopilotFlags(args, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gg autopilot: %v\n", err)
		os.Exit(2)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: c.logLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if c.acp {
		out := claimStdout()
		err = agent.ServeACP(ctx, os.Stdin, out, agent.ACPOptions{
			Version: version.Version, Model: c.model, Logger: logger,
			MCPURL: c.mcpURL, MCPToken: c.token, Limits: c.limits,
		})
		if err != nil {
			logger.Error("gg autopilot --acp stopped", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := runAgentCLI(ctx, c, logger, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gg autopilot: %v\n", err)
		os.Exit(1)
	}
}

// runAgentCLI runs one agent to completion: progress on errOut, the final answer on out.
func runAgentCLI(ctx context.Context, c autopilotCmd, logger *slog.Logger, out, errOut io.Writer) error {
	mc := agent.ModelConfigFromEnv(c.model)
	llm, err := agent.NewModel(ctx, mc)
	if err != nil {
		return err
	}
	be, err := agent.NewBackend(agent.BackendOptions{
		URL: c.mcpURL, Token: c.token, Limits: c.limits, Version: version.Version, Logger: logger,
	})
	if err != nil {
		return err
	}
	defer be.Close()

	task, err := withSpec(c.task, c.mode)
	if err != nil {
		return err
	}
	res, err := agent.Run(ctx, agent.Options{Model: llm, Backend: be, OutPath: c.out, MaxTurns: c.maxTurns}, c.mode, task, progressSink(errOut))
	if res.Text != "" {
		_, _ = fmt.Fprintln(out, res.Text)
	}
	if res.Saved != "" {
		_, _ = fmt.Fprintf(errOut, "saved %s\n", res.Saved)
	}
	return err
}

// withSpec inlines a spec file named as the task, so the model can read it.
func withSpec(task string, mode agent.Mode) (string, error) {
	if mode != agent.ModeGenerate || strings.ContainsAny(task, " \t\n") {
		return task, nil
	}
	info, err := os.Stat(task)
	if err != nil || !info.Mode().IsRegular() {
		return task, nil
	}
	if info.Size() > maxSpecBytes {
		return "", fmt.Errorf("%s is larger than %d KB; give a shorter spec or describe the API", task, maxSpecBytes>>10)
	}
	b, err := os.ReadFile(task)
	if err != nil {
		return "", err
	}
	return "Write a .http file for the API described by this spec (" + task + "):\n\n" + string(b), nil
}

// progressSink prints one short line per tool call, so a long run is not silent.
func progressSink(w io.Writer) agent.Sink {
	return func(ev agent.Event) {
		switch ev.Kind {
		case agent.KindToolCall:
			args, _ := json.Marshal(ev.Args)
			s := string(args)
			if len(s) > 120 {
				s = s[:120] + "…"
			}
			_, _ = fmt.Fprintf(w, "[%s] %s %s\n", time.Now().Format("15:04:05"), ev.Tool, s)
		case agent.KindToolResult:
			if e, ok := ev.Result["error"].(string); ok {
				_, _ = fmt.Fprintf(w, "    failed: %s\n", e)
			}
		}
	}
}
