// Package agent runs gg's LLM agents and its ACP server on top of the gg MCP tools.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	ggmcp "github.com/shyam-s00/gopher-glide/internal/mcp"
)

// BackendOptions say where the gg MCP tools live.
type BackendOptions struct {
	// URL is a running gg mcp daemon's /mcp endpoint; empty serves in-process.
	URL   string
	Token string

	// The remaining fields configure the in-process server only.
	Limits  ggmcp.Limits
	Version string
	Exe     string
	Logger  *slog.Logger
}

// Backend hands out MCP connections to gg's tools, whether in-process or remote.
type Backend struct {
	srv    *ggmcp.Server
	url    string
	httpc  *http.Client
	logger *slog.Logger

	mu      sync.Mutex
	clients []*sdk.ClientSession
}

// NewBackend starts an in-process gg server, or points at the daemon in o.URL.
func NewBackend(o BackendOptions) (*Backend, error) {
	b := &Backend{url: o.URL, logger: o.Logger}
	if b.logger == nil {
		b.logger = slog.New(slog.DiscardHandler)
	}
	if o.URL != "" {
		if !strings.HasPrefix(o.URL, "http://") && !strings.HasPrefix(o.URL, "https://") {
			return nil, fmt.Errorf("MCP URL must be http(s), got %q", o.URL)
		}
		b.httpc = &http.Client{Transport: bearer{token: o.Token, next: http.DefaultTransport}}
		return b, nil
	}
	limits := o.Limits
	if limits.MaxRPS == 0 {
		limits = ggmcp.DefaultLimits()
	}
	srv, err := ggmcp.NewServer(ggmcp.Options{Limits: limits, Version: o.Version, Exe: o.Exe, Logger: b.logger})
	if err != nil {
		return nil, err
	}
	b.srv = srv
	return b, nil
}

// Remote reports whether the tools run in another process.
func (b *Backend) Remote() bool { return b.srv == nil }

// Transport returns a fresh client transport to the tools.
func (b *Backend) Transport(ctx context.Context) (sdk.Transport, error) {
	if b.srv == nil {
		return &sdk.StreamableClientTransport{Endpoint: b.url, HTTPClient: b.httpc}, nil
	}
	client, server := sdk.NewInMemoryTransports()
	ss, err := b.srv.SDK.Connect(ctx, server, nil)
	if err != nil {
		return nil, fmt.Errorf("connect in-process gg server: %w", err)
	}
	go func() { _ = ss.Wait() }()
	return client, nil
}

// Client opens an MCP client session of its own; Close ends it.
func (b *Backend) Client(ctx context.Context) (*sdk.ClientSession, error) {
	t, err := b.Transport(ctx)
	if err != nil {
		return nil, err
	}
	c := sdk.NewClient(&sdk.Implementation{Name: "gg-autopilot", Version: "1"}, nil)
	cs, err := c.Connect(ctx, t, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to gg tools: %w", err)
	}
	b.mu.Lock()
	b.clients = append(b.clients, cs)
	b.mu.Unlock()
	return cs, nil
}

// Close ends every client it opened and stops the in-process server's runs.
func (b *Backend) Close() {
	b.mu.Lock()
	clients := b.clients
	b.clients = nil
	b.mu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
	if b.srv != nil {
		b.srv.Close()
	}
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (r bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.token == "" {
		return r.next.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+r.token)
	return r.next.RoundTrip(req)
}

// callTool calls a gg tool and decodes its structured result into out.
func callTool(ctx context.Context, cs *sdk.ClientSession, name string, args map[string]any, out any) error {
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if res.IsError {
		return fmt.Errorf("%s: %s", name, resultText(res))
	}
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil || string(raw) == "null" {
		return errors.New(name + ": the tool returned no structured result")
	}
	return json.Unmarshal(raw, out)
}

func resultText(res *sdk.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*sdk.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, " ")
}
