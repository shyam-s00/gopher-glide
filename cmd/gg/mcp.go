package main

import (
	"context"
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

	"github.com/shyam-s00/gopher-glide/internal/mcp"
	"github.com/shyam-s00/gopher-glide/internal/version"
)

type mcpOptions struct {
	limits   mcp.Limits
	logLevel slog.Level
}

// hostList is a repeatable flag that also accepts comma-separated values.
type hostList []string

func (h *hostList) String() string { return strings.Join(*h, ",") }

func (h *hostList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*h = append(*h, part)
		}
	}
	return nil
}

func parseMCPFlags(args []string, out io.Writer) (mcpOptions, error) {
	def := mcp.DefaultLimits()
	fs := flag.NewFlagSet("gg mcp", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		_, _ = fmt.Fprint(out, "Usage: gg mcp [flags]\n\nServe gg to AI agents over the Model Context Protocol (stdio).\n\nFlags:\n")
		fs.PrintDefaults()
	}

	maxRPS := fs.Int("max-rps", def.MaxRPS, "highest RPS any simulation may use")
	maxDur := fs.Duration("max-duration", def.MaxDuration, "longest duration any simulation may run")
	maxSessions := fs.Int("max-sessions", def.MaxSessions, "simulations that may run at the same time")
	var hosts hostList
	fs.Var(&hosts, "allow-host", "restrict targets to this host or host:port (repeatable or comma-separated; default any)")
	logLevel := fs.String("log-level", "info", "log verbosity on stderr: debug, info, warn, error")

	if err := fs.Parse(args); err != nil {
		return mcpOptions{}, err
	}
	if fs.NArg() > 0 {
		return mcpOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return mcpOptions{}, fmt.Errorf("invalid --log-level %q (use debug, info, warn or error)", *logLevel)
	}

	limits := mcp.Limits{MaxRPS: *maxRPS, MaxDuration: *maxDur, MaxSessions: *maxSessions, AllowHosts: hosts}
	if err := limits.Validate(); err != nil {
		return mcpOptions{}, err
	}
	return mcpOptions{limits: limits, logLevel: level}, nil
}

// claimStdout returns the real stdout for the protocol and points os.Stdout at
// stderr, so a stray fmt.Print can never corrupt the JSON-RPC stream.
func claimStdout() *os.File {
	out := os.Stdout
	os.Stdout = os.Stderr
	return out
}

// runMCPCmd serves MCP over stdio; stdout carries only protocol frames.
func runMCPCmd(args []string) {
	opts, err := parseMCPFlags(args, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gg mcp: %v\n", err)
		os.Exit(2)
	}

	protocolOut := claimStdout()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: opts.logLevel}))
	srv, err := mcp.NewServer(mcp.Options{Limits: opts.limits, Version: version.Version, Logger: logger})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gg mcp: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	logger.Info("gg mcp ready", "transport", "stdio", "max_rps", opts.limits.MaxRPS,
		"max_duration", opts.limits.MaxDuration, "max_sessions", opts.limits.MaxSessions)
	if err := srv.Serve(ctx, os.Stdin, protocolOut); err != nil {
		logger.Error("gg mcp stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("gg mcp stopped", "uptime", time.Since(start).Round(time.Millisecond))
}
