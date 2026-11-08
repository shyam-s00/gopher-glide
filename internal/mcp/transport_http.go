package mcp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// EndpointPath is where the Streamable HTTP endpoint is served.
const EndpointPath = "/mcp"

// MinTokenLength is the shortest accepted bearer token.
const MinTokenLength = 16

const httpShutdownGrace = 5 * time.Second

// HTTPOptions configure the Streamable HTTP transport.
type HTTPOptions struct {
	// Token, when set, must be sent by every client as a bearer token.
	Token string
	// AllowedOrigins lists browser origins (scheme://host[:port]) that may call
	// the server. A request carrying any other Origin header is refused.
	AllowedOrigins []string
	Logger         *slog.Logger
}

// IsLoopbackHost reports whether host names this machine's loopback interface.
func IsLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CheckBind refuses a configuration that would expose the server without
// authentication: anything other than loopback needs a token.
func CheckBind(bind, token string) error {
	if token != "" && len(token) < MinTokenLength {
		return fmt.Errorf("the auth token must be at least %d characters", MinTokenLength)
	}
	if !IsLoopbackHost(bind) && token == "" {
		return fmt.Errorf("refusing to listen on %q without authentication: give --auth-token (or GG_MCP_TOKEN), or bind to 127.0.0.1", bind)
	}
	return nil
}

// HTTPHandler returns the handler for the Streamable HTTP endpoint. loopback
// says whether the listener only accepts local connections, which turns on
// the Host check that stops DNS-rebinding from a web page.
func (s *Server) HTTPHandler(loopback bool, o HTTPOptions) http.Handler {
	inner := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.SDK }, &sdk.StreamableHTTPOptions{Logger: o.Logger})

	mux := http.NewServeMux()
	mux.Handle(EndpointPath, guard(inner, loopback, o))
	return mux
}

// guard runs the Host and Origin checks, then the token check, before the MCP handler.
func guard(next http.Handler, loopback bool, o HTTPOptions) http.Handler {
	origins := map[string]bool{}
	for _, a := range o.AllowedOrigins {
		origins[normalizeOrigin(a)] = true
	}
	var want [sha256.Size]byte
	if o.Token != "" {
		want = sha256.Sum256([]byte(o.Token))
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if loopback && !IsLoopbackHost(hostOnly(r.Host)) {
			http.Error(w, "forbidden: unexpected Host header", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !origins[normalizeOrigin(origin)] {
			http.Error(w, "forbidden: Origin not allowed (see --allow-origin)", http.StatusForbidden)
			return
		}
		if o.Token != "" {
			got := sha256.Sum256([]byte(bearer(r)))
			if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="gg mcp"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func normalizeOrigin(o string) string {
	u, err := url.Parse(strings.TrimSpace(o))
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimSpace(o))
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// ServeHTTP serves the endpoint on ln until ctx is cancelled. On shutdown every
// running simulation is stopped first, then in-flight requests get a short
// grace period.
func (s *Server) ServeHTTP(ctx context.Context, ln net.Listener, o HTTPOptions) error {
	loopback := false
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		loopback = tcp.IP.IsLoopback()
	}
	srv := &http.Server{Handler: s.HTTPHandler(loopback, o), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		s.Close()
		return err
	case <-ctx.Done():
	}
	s.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), httpShutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		_ = srv.Close()
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
