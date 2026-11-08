package mcp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
	"github.com/shyam-s00/gopher-glide/internal/mcp/session/fakegg"
)

const goodToken = "0123456789abcdef-token"

// Edge 31: the server will not listen beyond loopback without authentication.
func TestCheckBind_Table(t *testing.T) {
	cases := []struct {
		bind, token, err string
	}{
		{"127.0.0.1", "", ""},
		{"localhost", "", ""},
		{"::1", "", ""},
		{"[::1]", "", ""},
		{"127.0.0.1", goodToken, ""},
		{"0.0.0.0", "", "without authentication"},
		{"::", "", "without authentication"},
		{"", "", "without authentication"},
		{"192.168.1.20", "", "without authentication"},
		{"gg.example.com", "", "without authentication"},
		{"0.0.0.0", goodToken, ""},
		{"0.0.0.0", "short", "at least 16"},
		{"127.0.0.1", "short", "at least 16"},
	}
	for _, c := range cases {
		err := CheckBind(c.bind, c.token)
		if (c.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.err)) {
			t.Errorf("CheckBind(%q, %q) = %v, want %q", c.bind, c.token, err, c.err)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LOCALHOST": true, "127.0.0.1": true, "127.5.5.5": true, "::1": true, "[::1]": true,
		"0.0.0.0": false, "10.0.0.1": false, "evil.com": false, "localhost.evil.com": false, "127.0.0.1.evil.com": false, "": false,
	} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// do sends a request through the guard in front of a stub handler.
func do(h http.Handler, host, origin, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "http://"+host+EndpointPath, strings.NewReader("{}"))
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var stub = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

// Edge 32: a web page cannot reach a loopback server by rebinding its DNS name.
func TestGuard_HostAndOrigin(t *testing.T) {
	loop := guard(stub, true, HTTPOptions{AllowedOrigins: []string{"https://app.example.com"}})
	open := guard(stub, false, HTTPOptions{Token: goodToken})

	cases := []struct {
		name, host, origin string
		h                  http.Handler
		auth               string
		want               int
	}{
		{"loopback ip", "127.0.0.1:8123", "", loop, "", 204},
		{"localhost", "localhost:8123", "", loop, "", 204},
		{"ipv6 loopback", "[::1]:8123", "", loop, "", 204},
		{"rebound dns name", "rebind.evil.com:8123", "", loop, "", 403},
		{"look-alike", "localhost.evil.com", "", loop, "", 403},
		{"browser page on another site", "127.0.0.1:8123", "https://evil.example", loop, "", 403},
		{"browser page on localhost", "127.0.0.1:8123", "http://localhost:3000", loop, "", 403},
		{"opaque origin", "127.0.0.1:8123", "null", loop, "", 403},
		{"listed origin", "127.0.0.1:8123", "https://app.example.com", loop, "", 204},
		{"listed origin, other case", "127.0.0.1:8123", "HTTPS://App.Example.com", loop, "", 204},
		{"listed host, other scheme", "127.0.0.1:8123", "http://app.example.com", loop, "", 403},
		{"non-loopback ignores host", "gg.internal:8123", "", open, "Bearer " + goodToken, 204},
		{"non-loopback still checks origin", "gg.internal:8123", "https://evil.example", open, "Bearer " + goodToken, 403},
	}
	for _, c := range cases {
		if got := do(c.h, c.host, c.origin, c.auth).Code; got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
}

func TestGuard_BearerToken(t *testing.T) {
	g := guard(stub, true, HTTPOptions{Token: goodToken})
	for name, c := range map[string]struct {
		auth string
		want int
	}{
		"right":           {"Bearer " + goodToken, 204},
		"scheme any case": {"bearer " + goodToken, 204},
		"missing":         {"", 401},
		"wrong":           {"Bearer " + goodToken + "x", 401},
		"prefix of right": {"Bearer " + goodToken[:10], 401},
		"empty bearer":    {"Bearer ", 401},
		"other scheme":    {"Basic " + goodToken, 401},
		"token only":      {goodToken, 401},
	} {
		rec := do(g, "127.0.0.1:1", "", c.auth)
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, c.want)
		}
		if c.want == 401 && rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: 401 must say how to authenticate", name)
		}
	}
	// A rebinding attempt is refused before the token is even looked at.
	if got := do(g, "evil.com", "", "Bearer "+goodToken).Code; got != 403 {
		t.Errorf("rebinding with a valid token: status %d, want 403", got)
	}
	// With no token configured, no Authorization header is needed.
	if got := do(guard(stub, true, HTTPOptions{}), "127.0.0.1:1", "", "").Code; got != 204 {
		t.Errorf("no token configured: status %d", got)
	}
}

// httpServer runs the fake-child server on a loopback port and returns its URL.
func httpServer(t *testing.T, scenario string, o HTTPOptions) (url string, srv *Server, stop func() error) {
	t.Helper()
	t.Setenv(fakegg.Env, scenario)
	srv, err := NewServer(Options{
		Limits: DefaultLimits(), Version: "test", Exe: osArgs0(),
		Sessions: session.ManagerOptions{StartTimeout: 5 * time.Second, Stop: session.StopOptions{Graceful: 2 * time.Second, Term: time.Second}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ServeHTTP(ctx, ln, o) }()
	var stopped bool
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(15 * time.Second):
			t.Error("ServeHTTP did not return after cancel")
			return nil
		}
	}
	t.Cleanup(func() { _ = stop() })
	return "http://" + ln.Addr().String() + EndpointPath, srv, stop
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func httpClient(t *testing.T, url, token string) (*sdk.ClientSession, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	tr := &sdk.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearerTransport{token}}, MaxRetries: -1, DisableStandaloneSSE: true,
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, tr, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

func TestHTTP_HandshakeListsAllToolsAndAnswersCalls(t *testing.T) {
	url, _, _ := httpServer(t, "normal", HTTPOptions{Token: goodToken})
	cs, err := httpClient(t, url, goodToken)
	if err != nil {
		t.Fatal(err)
	}
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil || len(list.Tools) != len(toolNames) {
		t.Fatalf("tools = %d, err = %v (want %d)", len(list.Tools), err, len(toolNames))
	}
	var rep struct {
		OK bool `json:"ok"`
	}
	res := call(t, cs, "validate_http_file", map[string]any{"content": "GET http://127.0.0.1:9/ok\n"}, &rep)
	if res.IsError || !rep.OK {
		t.Fatalf("validate over HTTP: %v %s", rep, errText(res))
	}
}

func TestHTTP_RejectsWithoutOrWithWrongToken(t *testing.T) {
	url, _, _ := httpServer(t, "normal", HTTPOptions{Token: goodToken})
	for name, token := range map[string]string{"none": "", "wrong": goodToken + "x"} {
		if cs, err := httpClient(t, url, token); err == nil {
			_ = cs.Close()
			t.Errorf("%s token: connection must be refused", name)
		}
	}
}

func rawRequest(t *testing.T, method, url, host, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestHTTP_RefusesRebindingAndBrowserOriginsOnTheWire(t *testing.T) {
	url, _, _ := httpServer(t, "normal", HTTPOptions{})
	if got := rawRequest(t, http.MethodPost, url, "rebind.evil.com", "").StatusCode; got != http.StatusForbidden {
		t.Errorf("rebound Host: status %d", got)
	}
	if got := rawRequest(t, http.MethodPost, url, "", "https://evil.example").StatusCode; got != http.StatusForbidden {
		t.Errorf("foreign Origin: status %d", got)
	}
	if got := rawRequest(t, http.MethodPost, strings.TrimSuffix(url, EndpointPath)+"/other", "", "").StatusCode; got != http.StatusNotFound {
		t.Errorf("other path: status %d", got)
	}
	resp := rawRequest(t, http.MethodPost, url, "", "")
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("a plain local request must get through, status %d", resp.StatusCode)
	}
}

// A session id is usable from any connection that holds the token.
func TestHTTP_SessionsAreSharedAcrossConnections(t *testing.T) {
	url, _, _ := httpServer(t, "normal", HTTPOptions{Token: goodToken})
	a, err := httpClient(t, url, goodToken)
	if err != nil {
		t.Fatal(err)
	}
	b, err := httpClient(t, url, goodToken)
	if err != nil {
		t.Fatal(err)
	}

	st := startOK(t, a)
	var tel session.Telemetry
	waitTelemetry(t, b, st.SessionID, &tel, func() bool { return tel.State == session.Running })

	var out SteerOutput
	if res := call(t, b, "steer_traffic", map[string]any{"session_id": st.SessionID, "action": "adjust_rps", "delta": 3}, &out); res.IsError {
		t.Fatalf("steer from the second connection: %s", errText(res))
	}
	call(t, a, "get_live_telemetry", map[string]any{"session_id": st.SessionID}, &tel)
	if len(tel.Steers) != 1 {
		t.Errorf("connection A should see B's steer: %+v", tel.Steers)
	}
	var final session.Telemetry
	call(t, b, "stop_simulation", map[string]any{"session_id": st.SessionID}, &final)
	if final.State != session.Stopped {
		t.Fatalf("stopped by the other connection: %+v", final)
	}
	// The session cap is shared too.
	call(t, a, "get_live_telemetry", map[string]any{}, &tel)
	if tel.SessionID != st.SessionID {
		t.Errorf("A resolved %q", tel.SessionID)
	}
}

// Shutting the server down stops every simulation it started, over HTTP as over stdio.
func TestHTTP_ShutdownStopsRunningSimulations(t *testing.T) {
	url, srv, stop := httpServer(t, "normal", HTTPOptions{})
	cs, err := httpClient(t, url, "")
	if err != nil {
		t.Fatal(err)
	}
	st := startOK(t, cs)
	sess, err := srv.sessions.Get(st.SessionID)
	if err != nil {
		t.Fatal(err)
	}

	begin := time.Now()
	if err := stop(); err != nil {
		t.Fatalf("ServeHTTP returned %v", err)
	}
	select {
	case <-sess.Done():
	default:
		t.Fatal("the simulation child outlived the server")
	}
	if got := sess.State(); got != session.Stopped {
		t.Errorf("state = %s", got)
	}
	if d := time.Since(begin); d > 12*time.Second {
		t.Errorf("shutdown took %v", d)
	}
	if _, err := net.DialTimeout("tcp", strings.TrimSuffix(strings.TrimPrefix(url, "http://"), EndpointPath), time.Second); err == nil {
		t.Error("the listener should be closed")
	}
}
