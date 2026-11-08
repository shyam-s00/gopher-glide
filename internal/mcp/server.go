// Package mcp exposes gg to AI agents as a Model Context Protocol server.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
)

// Options configures a Server.
type Options struct {
	Limits  Limits
	Version string
	// Logger receives server activity. It must not write to the transport's
	// stdout; nil discards logs.
	Logger *slog.Logger
	// Exe is the gg binary simulations are launched from; default is this one.
	Exe string
	// Sessions tunes timeouts and retention; MaxSessions comes from Limits.
	Sessions session.ManagerOptions
}

const instructions = `gg is an HTTP load-testing tool. Read the gg://docs/http-syntax resource before writing a .http file, ` +
	`gg://schema/gg-yaml for config files, and gg://profiles/catalog to choose a traffic shape.`

// toolNames lists every tool the server will offer; prompts may mention only these.
var toolNames = []string{
	"validate_http_file",
	"run_journey_dry_run",
	"generate_config",
	"start_simulation",
	"stop_simulation",
	"get_live_telemetry",
	"steer_traffic",
	"snap_capture",
	"snap_diff",
	"snap_assert",
}

// Server is an MCP server together with the simulations it supervises.
type Server struct {
	// SDK is the underlying protocol server.
	SDK      *sdk.Server
	sessions *session.Manager
}

// NewServer builds a server with all resources, prompts and tools registered.
func NewServer(o Options) (*Server, error) {
	if err := o.Limits.Validate(); err != nil {
		return nil, fmt.Errorf("invalid limits: %w", err)
	}
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	version := o.Version
	if version == "" {
		version = "dev"
	}
	exe := o.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return nil, fmt.Errorf("locate the gg executable: %w", err)
		}
	}
	o.Sessions.MaxSessions = o.Limits.MaxSessions
	mgr := session.NewManager(o.Sessions)

	s := sdk.NewServer(
		&sdk.Implementation{Name: "gg", Title: "Gopher Glide", Version: version},
		&sdk.ServerOptions{Instructions: instructions, Logger: logger},
	)
	if err := registerResources(s); err != nil {
		return nil, err
	}
	registerAuthorTools(s, o.Limits)
	registerSimTools(s, mgr, o.Limits, exe)
	registerSnapTools(s, mgr)
	registerPrompts(s)
	return &Server{SDK: s, sessions: mgr}, nil
}

// Serve runs the server over newline-delimited JSON-RPC on in/out until the
// client disconnects (EOF on in) or ctx is cancelled; both are clean
// shutdowns. Either way every running simulation is stopped before it returns.
func (s *Server) Serve(ctx context.Context, in io.ReadCloser, out io.WriteCloser) error {
	defer s.Close()
	err := s.SDK.Run(ctx, &sdk.IOTransport{Reader: in, Writer: out})
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// Close stops all running simulations and refuses new ones.
func (s *Server) Close() {
	s.sessions.Shutdown(context.Background())
}
