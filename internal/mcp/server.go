// Package mcp exposes gg to AI agents as a Model Context Protocol server.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configures a Server.
type Options struct {
	Limits  Limits
	Version string
	// Logger receives server activity. It must not write to the transport's
	// stdout; nil discards logs.
	Logger *slog.Logger
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

// NewServer builds a server with all resources and prompts registered.
func NewServer(o Options) (*sdk.Server, error) {
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

	s := sdk.NewServer(
		&sdk.Implementation{Name: "gg", Title: "Gopher Glide", Version: version},
		&sdk.ServerOptions{Instructions: instructions, Logger: logger},
	)
	if err := registerResources(s); err != nil {
		return nil, err
	}
	registerPrompts(s)
	return s, nil
}

// Serve runs s over newline-delimited JSON-RPC on in/out until the client
// disconnects (EOF on in) or ctx is cancelled. Both are clean shutdowns.
func Serve(ctx context.Context, s *sdk.Server, in io.ReadCloser, out io.WriteCloser) error {
	err := s.Run(ctx, &sdk.IOTransport{Reader: in, Writer: out})
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
