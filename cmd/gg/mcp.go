package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	neturl "net/url"
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
	// exitOnEOF stops the daemon when its stdin closes, so it dies with the IDE.
	exitOnEOF bool

	// attach shares a running daemon over stdio instead of serving; attachURL
	// names it, or is empty to look it up for the working directory.
	attach    bool
	attachURL string
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
		_, _ = fmt.Fprint(out, "Usage: gg mcp [flags]\n       gg mcp --attach [url]\n\nServe gg to AI agents over the Model Context Protocol.\nStdio by default; --port serves Streamable HTTP at /mcp (and the IDE plugin bridge at /plugin/) instead.\n--attach connects a stdio client to a running daemon, found by working directory when no url is given,\nand serves in-process when there is none.\n\nFlags:\n")
		fs.PrintDefaults()
	}

	maxRPS := fs.Int("max-rps", def.MaxRPS, "highest RPS any simulation may use")
	maxDur := fs.Duration("max-duration", def.MaxDuration, "longest duration any simulation may run")
	maxSessions := fs.Int("max-sessions", def.MaxSessions, "simulations an agent may run at the same time")
	maxUserSessions := fs.Int("max-user-sessions", def.MaxUserSessions, "simulations the IDE plugin may run at the same time (with --port)")
	var hosts hostList
	fs.Var(&hosts, "allow-host", "restrict targets to this host or host:port (repeatable or comma-separated; default any)")
	logLevel := fs.String("log-level", "info", "log verbosity on stderr: debug, info, warn, error")
	port := fs.Int("port", 0, "serve Streamable HTTP on this port (0 picks a free one) instead of stdio")
	bind := fs.String("bind", "127.0.0.1", "address to listen on with --port; anything but loopback needs an auth token")
	token := fs.String("auth-token", "", "bearer token clients must send with --port (or set "+tokenEnv+"; min 16 characters)")
	var origins hostList
	fs.Var(&origins, "allow-origin", "browser origin allowed to call the HTTP endpoint, e.g. https://example.com (repeatable; default none)")

	exitOnEOF := fs.Bool("exit-on-stdin-eof", false, "with --port, stop every simulation and exit when stdin closes (for a daemon an IDE supervises)")
	attach := fs.Bool("attach", false, "connect this stdio client to a running gg mcp daemon (url optional) instead of serving")

	if err := fs.Parse(args); err != nil {
		return mcpOptions{}, err
	}
	if fs.NArg() > 1 || (fs.NArg() == 1 && !*attach) {
		return mcpOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(fs.NArg()-1))
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
	if *attach {
		for _, name := range []string{"port", "bind", "allow-origin", "exit-on-stdin-eof"} {
			if set[name] {
				return mcpOptions{}, fmt.Errorf("--%s cannot be combined with --attach", name)
			}
		}
		if fs.NArg() == 1 {
			if _, err := neturl.ParseRequestURI(fs.Arg(0)); err != nil || !strings.HasPrefix(fs.Arg(0), "http") {
				return mcpOptions{}, fmt.Errorf("--attach takes an http(s) URL such as http://127.0.0.1:5000/mcp, got %q", fs.Arg(0))
			}
		}
	}
	if *exitOnEOF && !useHTTP {
		return mcpOptions{}, errors.New("--exit-on-stdin-eof only applies with --port")
	}
	if !useHTTP {
		for _, name := range []string{"bind", "allow-origin"} {
			if set[name] {
				return mcpOptions{}, fmt.Errorf("--%s only applies with --port", name)
			}
		}
		if set["auth-token"] && !*attach {
			return mcpOptions{}, errors.New("--auth-token only applies with --port or --attach")
		}
	} else {
		if *port < 0 || *port > 65535 {
			return mcpOptions{}, fmt.Errorf("--port must be between 0 and 65535, got %d", *port)
		}
		if err := mcp.CheckBind(*bind, *token); err != nil {
			return mcpOptions{}, err
		}
	}

	limits := mcp.Limits{MaxRPS: *maxRPS, MaxDuration: *maxDur, MaxSessions: *maxSessions, MaxUserSessions: *maxUserSessions, AllowHosts: hosts}
	if err := limits.Validate(); err != nil {
		return mcpOptions{}, err
	}
	o := mcpOptions{limits: limits, logLevel: level, http: useHTTP, port: *port, bind: *bind, token: *token, origins: origins, exitOnEOF: *exitOnEOF, attach: *attach}
	if *attach && fs.NArg() == 1 {
		o.attachURL = fs.Arg(0)
	}
	return o, nil
}

// claimStdout returns the real stdout for the protocol and points os.Stdout at
// stderr, so a stray fmt.Print can never corrupt the JSON-RPC stream.
func claimStdout() *os.File {
	out := os.Stdout
	os.Stdout = os.Stderr
	return out
}

// runMCPCmd serves MCP over stdio (stdout carries only protocol frames) or,
// with --port, over Streamable HTTP. With --attach it bridges stdio to a
// running daemon instead.
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if opts.attach {
		if url, token, ok := attachTarget(opts, logger); ok {
			if err := mcp.Attach(ctx, os.Stdin, protocolOut, mcp.AttachOptions{Endpoint: url, Token: token, Logger: logger}); err != nil {
				logger.Error("gg mcp attach stopped", "error", err)
				os.Exit(1)
			}
			return
		} else if opts.attachURL != "" {
			_, _ = fmt.Fprintf(os.Stderr, "gg mcp: cannot reach a gg mcp daemon at %s\n", opts.attachURL)
			os.Exit(1)
		}
		logger.Info("no gg mcp daemon is running for this directory; serving in-process")
	}

	srv, err := mcp.NewServer(mcp.Options{Limits: opts.limits, Version: version.Version, Logger: logger})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gg mcp: %v\n", err)
		os.Exit(1)
	}

	start := time.Now()
	if opts.http {
		if opts.exitOnEOF {
			ctx = cancelOnStdinEOF(ctx, logger)
		}
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

// cancelOnStdinEOF returns a context that ends when stdin closes: the parent
// that supervises this daemon is gone, so it stops its runs and exits.
func cancelOnStdinEOF(parent context.Context, logger *slog.Logger) context.Context {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		logger.Info("stdin closed; stopping")
		cancel()
	}()
	return ctx
}

// attachTarget picks the daemon to attach to: the URL given, else the one
// registered for the working directory if its process is alive and reachable.
func attachTarget(opts mcpOptions, logger *slog.Logger) (url, token string, ok bool) {
	if opts.attachURL != "" {
		return opts.attachURL, opts.token, reachable(opts.attachURL)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", "", false
	}
	d, found := mcp.FindDaemon(wd)
	if !found {
		return "", "", false
	}
	if !reachable(d.URL) {
		logger.Warn("ignoring a daemon record that does not answer", "url", d.URL, "pid", d.PID)
		return "", "", false
	}
	return d.URL, d.Token, true
}

// reachable reports whether something listens at the URL's host and port.
func reachable(raw string) bool {
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(u.Hostname(), port), time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// connectURL turns a listener address into a URL a local client can dial.
func connectURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String() + mcp.EndpointPath
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + mcp.EndpointPath
}

// serveHTTP listens on the configured address and serves until ctx is cancelled.
// It announces the bound URL as one JSON line on stdout and records the daemon
// where `gg mcp --attach` can find it.
func serveHTTP(ctx context.Context, srv *mcp.Server, opts mcpOptions, logger *slog.Logger) {
	ln, err := net.Listen("tcp", net.JoinHostPort(opts.bind, strconv.Itoa(opts.port)))
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gg mcp: %v\n", err)
		os.Exit(1)
	}
	endpoint := connectURL(ln.Addr())
	logger.Info("gg mcp ready", "transport", "http", "url", endpoint,
		"auth", opts.token != "", "max_rps", opts.limits.MaxRPS, "max_duration", opts.limits.MaxDuration,
		"max_sessions", opts.limits.MaxSessions, "max_user_sessions", opts.limits.MaxUserSessions)

	remove := func() {}
	if wd, err := os.Getwd(); err == nil {
		if rm, err := mcp.RegisterDaemon(wd, mcp.Daemon{URL: endpoint, Token: opts.token, PID: os.Getpid()}); err != nil {
			logger.Warn("could not record this daemon for --attach", "error", err)
		} else {
			remove = rm
		}
	}
	line, _ := json.Marshal(map[string]any{"event": "listening", "url": endpoint, "pid": os.Getpid()})
	_, _ = fmt.Fprintln(os.Stdout, string(line))

	err = srv.ServeHTTP(ctx, ln, mcp.HTTPOptions{Token: opts.token, AllowedOrigins: opts.origins, Logger: logger})
	remove()
	if err != nil {
		logger.Error("gg mcp stopped", "error", err)
		os.Exit(1)
	}
}
