package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/mcp"
	"github.com/shyam-s00/gopher-glide/internal/version"
)

type mcpOptions struct {
	limits   mcp.Limits
	logLevel slog.Level

	// HTTP transport; used only when http is true.
	http    bool
	port    int
	bind    string
	token   string
	origins []string
}

const tokenEnv = "GG_MCP_TOKEN"

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
		_, _ = fmt.Fprint(out, "Usage: gg mcp [flags]\n\nServe gg to AI agents over the Model Context Protocol.\nStdio by default; --port serves Streamable HTTP at /mcp instead.\n\nFlags:\n")
		fs.PrintDefaults()
	}

	maxRPS := fs.Int("max-rps", def.MaxRPS, "highest RPS any simulation may use")
	maxDur := fs.Duration("max-duration", def.MaxDuration, "longest duration any simulation may run")
	maxSessions := fs.Int("max-sessions", def.MaxSessions, "simulations that may run at the same time")
	var hosts hostList
	fs.Var(&hosts, "allow-host", "restrict targets to this host or host:port (repeatable or comma-separated; default any)")
	logLevel := fs.String("log-level", "info", "log verbosity on stderr: debug, info, warn, error")
	port := fs.Int("port", 0, "serve Streamable HTTP on this port (0 picks a free one) instead of stdio")
	bind := fs.String("bind", "127.0.0.1", "address to listen on with --port; anything but loopback needs an auth token")
	token := fs.String("auth-token", "", "bearer token clients must send with --port (or set "+tokenEnv+"; min 16 characters)")
	var origins hostList
	fs.Var(&origins, "allow-origin", "browser origin allowed to call the HTTP endpoint, e.g. https://example.com (repeatable; default none)")

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

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	useHTTP := set["port"]
	if *token == "" {
		*token = os.Getenv(tokenEnv)
	}
	if !useHTTP {
		for _, name := range []string{"bind", "auth-token", "allow-origin"} {
			if set[name] {
				return mcpOptions{}, fmt.Errorf("--%s only applies with --port", name)
			}
		}
	} else {
		if *port < 0 || *port > 65535 {
			return mcpOptions{}, fmt.Errorf("--port must be between 0 and 65535, got %d", *port)
		}
		if err := mcp.CheckBind(*bind, *token); err != nil {
			return mcpOptions{}, err
		}
	}

	limits := mcp.Limits{MaxRPS: *maxRPS, MaxDuration: *maxDur, MaxSessions: *maxSessions, AllowHosts: hosts}
	if err := limits.Validate(); err != nil {
		return mcpOptions{}, err
	}
	return mcpOptions{limits: limits, logLevel: level, http: useHTTP, port: *port, bind: *bind, token: *token, origins: origins}, nil
}

// claimStdout returns the real stdout for the protocol and points os.Stdout at
// stderr, so a stray fmt.Print can never corrupt the JSON-RPC stream.
func claimStdout() *os.File {
	out := os.Stdout
	os.Stdout = os.Stderr
	return out
}

// runMCPCmd serves MCP over stdio (stdout carries only protocol frames) or,
// with --port, over Streamable HTTP.
func runMCPCmd(args []string) {
	opts, err := parseMCPFlags(args, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gg mcp: %v\n", err)
		os.Exit(2)
	}

	var protocolOut *os.File
	if !opts.http {
		protocolOut = claimStdout()
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: opts.logLevel}))
	srv, err := mcp.NewServer(mcp.Options{Limits: opts.limits, Version: version.Version, Logger: logger})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gg mcp: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	if opts.http {
		serveHTTP(ctx, srv, opts, logger)
		logger.Info("gg mcp stopped", "uptime", time.Since(start).Round(time.Millisecond))
		return
	}
	logger.Info("gg mcp ready", "transport", "stdio", "max_rps", opts.limits.MaxRPS,
		"max_duration", opts.limits.MaxDuration, "max_sessions", opts.limits.MaxSessions)
	if err := srv.Serve(ctx, os.Stdin, protocolOut); err != nil {
		logger.Error("gg mcp stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("gg mcp stopped", "uptime", time.Since(start).Round(time.Millisecond))
}

// serveHTTP listens on the configured address and serves until ctx is cancelled.
func serveHTTP(ctx context.Context, srv *mcp.Server, opts mcpOptions, logger *slog.Logger) {
	ln, err := net.Listen("tcp", net.JoinHostPort(opts.bind, strconv.Itoa(opts.port)))
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gg mcp: %v\n", err)
		os.Exit(1)
	}
	logger.Info("gg mcp ready", "transport", "http", "url", "http://"+ln.Addr().String()+mcp.EndpointPath,
		"auth", opts.token != "", "max_rps", opts.limits.MaxRPS, "max_duration", opts.limits.MaxDuration, "max_sessions", opts.limits.MaxSessions)
	err = srv.ServeHTTP(ctx, ln, mcp.HTTPOptions{Token: opts.token, AllowedOrigins: opts.origins, Logger: logger})
	if err != nil {
		logger.Error("gg mcp stopped", "error", err)
		os.Exit(1)
	}
}
