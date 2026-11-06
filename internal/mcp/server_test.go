package mcp

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func newTestServer(t *testing.T) *sdk.Server {
	t.Helper()
	s, err := NewServer(Options{Limits: DefaultLimits(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// connect returns a client session wired to a fresh server in memory.
func connect(t *testing.T) *sdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ct, st := sdk.NewInMemoryTransports()
	ss, err := newTestServer(t).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Wait() })
	return cs
}

func TestInitialize_ReportsIdentityAndCapabilities(t *testing.T) {
	cs := connect(t)
	init := cs.InitializeResult()
	if init.ServerInfo.Name != "gg" || init.ServerInfo.Version != "test" {
		t.Errorf("server info = %+v", init.ServerInfo)
	}
	if init.Capabilities.Resources == nil || init.Capabilities.Prompts == nil {
		t.Errorf("resources and prompts must be advertised: %+v", init.Capabilities)
	}
	if !strings.Contains(init.Instructions, "gg://docs/http-syntax") {
		t.Errorf("instructions should point at the docs resource: %q", init.Instructions)
	}
}

func TestNewServer_RejectsInvalidLimits(t *testing.T) {
	if _, err := NewServer(Options{Limits: Limits{}}); err == nil {
		t.Fatal("zero limits must be rejected")
	}
}

func TestLimits_Validate(t *testing.T) {
	ok := DefaultLimits()
	cases := []struct {
		name string
		mut  func(*Limits)
		want string
	}{
		{"defaults ok", func(*Limits) {}, ""},
		{"rps", func(l *Limits) { l.MaxRPS = 0 }, "max-rps"},
		{"duration", func(l *Limits) { l.MaxDuration = 0 }, "max-duration"},
		{"sessions", func(l *Limits) { l.MaxSessions = -1 }, "max-sessions"},
		{"host with path", func(l *Limits) { l.AllowHosts = []string{"a.com/x"} }, "allow-host"},
		{"empty host", func(l *Limits) { l.AllowHosts = []string{" "} }, "allow-host"},
		{"host:port ok", func(l *Limits) { l.AllowHosts = []string{"a.com:8080"} }, ""},
		{"several errors joined", func(l *Limits) { l.MaxRPS, l.MaxSessions = 0, 0 }, "max-sessions"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := ok
			c.mut(&l)
			err := l.Validate()
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want containing %q", err, c.want)
			}
		})
	}
}

func TestServe_StdinEOFEndsCleanly(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, outR) }()

	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), newTestServer(t), inR, outW) }()

	_ = inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EOF should be a clean shutdown, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after stdin EOF")
	}
}

func TestServe_ContextCancelEndsCleanly(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	defer inW.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, newTestServer(t), inR, outW) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancellation should be a clean shutdown, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancellation")
	}
}
