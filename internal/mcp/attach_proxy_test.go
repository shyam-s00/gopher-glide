package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
	"github.com/shyam-s00/gopher-glide/internal/mcp/session/fakegg"
)

// seen is one request the stub or daemon received.
type seen struct {
	Method, Session, Protocol, RPC string
	Done                           time.Time
}

type recorder struct {
	mu   sync.Mutex
	reqs []seen
}

func (r *recorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s := seen{Method: req.Method, Session: req.Header.Get(sessionHeader), Protocol: req.Header.Get(protocolHeader)}
		if req.Method == http.MethodPost {
			body, _ := io.ReadAll(req.Body)
			req.Body = io.NopCloser(bytes.NewReader(body))
			var m struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &m)
			s.RPC = m.Method
		}
		r.mu.Lock()
		r.reqs = append(r.reqs, s)
		i := len(r.reqs) - 1
		r.mu.Unlock()
		next.ServeHTTP(w, req)
		r.mu.Lock()
		r.reqs[i].Done = time.Now()
		r.mu.Unlock()
	})
}

func (r *recorder) find(method, rpc string) []seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []seen
	for _, s := range r.reqs {
		if s.Method == method && (rpc == "" || s.RPC == rpc) {
			out = append(out, s)
		}
	}
	return out
}

// bridgeRig is a real daemon (fake child), recorded, with an SDK client wired to it through Attach.
type bridgeRig struct {
	base     string
	endpoint string
	rec      *recorder
	client   *sdk.ClientSession
	err      error // why the handshake failed, when client is nil
	attachCh chan error
}

func newBridgeRig(t *testing.T, scenario, serverToken, bridgeToken string) *bridgeRig {
	t.Helper()
	t.Setenv(fakegg.Env, scenario)
	srv, err := NewServer(Options{
		Limits: DefaultLimits(), Version: "test", Exe: osArgs0(),
		Sessions: session.ManagerOptions{StartTimeout: 5 * time.Second, Stop: session.StopOptions{Graceful: 2 * time.Second, Term: time.Second}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: rec.wrap(srv.HTTPHandler(true, HTTPOptions{Token: serverToken})), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { srv.Close(); _ = hs.Close() })

	base := "http://" + ln.Addr().String()
	rig := &bridgeRig{base: base, endpoint: base + EndpointPath, rec: rec, attachCh: make(chan error, 1)}
	rig.connect(t, bridgeToken)
	return rig
}

func (rig *bridgeRig) connect(t *testing.T, token string) {
	t.Helper()
	toBridgeR, toBridgeW := io.Pipe()
	fromBridgeR, fromBridgeW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		err := Attach(ctx, toBridgeR, fromBridgeW, AttachOptions{Endpoint: rig.endpoint, Token: token})
		_ = fromBridgeW.Close()
		rig.attachCh <- err
	}()
	cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
	defer ccancel()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "stdio-client", Version: "1"}, nil).
		Connect(cctx, &sdk.IOTransport{Reader: fromBridgeR, Writer: toBridgeW}, nil)
	if err != nil {
		rig.client, rig.err = nil, err
		return
	}
	rig.client = cs
	t.Cleanup(func() { _ = cs.Close() })
}

func TestAttach_FullRunThroughTheBridge(t *testing.T) {
	rig := newBridgeRig(t, "normal", pluginToken, pluginToken)
	if rig.client == nil {
		t.Fatalf("handshake through the bridge failed: %v", rig.err)
	}
	cs := rig.client

	list, err := cs.ListTools(context.Background(), nil)
	if err != nil || len(list.Tools) != len(toolNames) {
		t.Fatalf("tools = %v, %v", len(list.Tools), err)
	}

	var started StartOutput
	if res := call(t, cs, "start_simulation", startArgs(), &started); res.IsError {
		t.Fatal(errText(res))
	}

	var tel session.Telemetry
	begin := time.Now()
	res := call(t, cs, "get_live_telemetry", map[string]any{"wait_s": 1.5}, &tel)
	if res.IsError || tel.WindowSummary == nil || tel.WindowSummary.WakeReason == "" {
		t.Fatalf("a blocking telemetry call through the bridge: %v %+v", errText(res), tel.WindowSummary)
	}
	if time.Since(begin) < 300*time.Millisecond && tel.WindowSummary.WakeReason == session.WakeTimer {
		t.Errorf("returned in %s for a timer wake", time.Since(begin))
	}

	var stopped session.Telemetry
	if res := call(t, cs, "stop_simulation", map[string]any{}, &stopped); res.IsError || stopped.State != session.Stopped {
		t.Fatalf("stop: %v %+v", errText(res), stopped)
	}

	// Every request after the handshake carries the session and negotiated protocol version.
	inits := rig.rec.find(http.MethodPost, "initialize")
	if len(inits) != 1 || inits[0].Session != "" {
		t.Fatalf("initialize requests = %+v", inits)
	}
	calls := rig.rec.find(http.MethodPost, "tools/call")
	if len(calls) < 3 {
		t.Fatalf("tool calls seen = %d", len(calls))
	}
	for _, c := range calls {
		if c.Session == "" || c.Protocol == "" {
			t.Errorf("a call lacks the session or protocol header: %+v", c)
		}
	}
	if gets := rig.rec.find(http.MethodGet, ""); len(gets) == 0 || gets[0].Session == "" {
		t.Errorf("the standalone stream was not opened with the session: %+v", gets)
	}

	_ = cs.Close()
	select {
	case err := <-rig.attachCh:
		if err != nil {
			t.Errorf("attach returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Attach did not return once the client closed")
	}
	if dels := rig.rec.find(http.MethodDelete, ""); len(dels) != 1 {
		t.Errorf("the daemon session should be ended once: %+v", dels)
	}
}

func TestAttach_SessionsLiveInTheDaemonSoThePluginSeesThem(t *testing.T) {
	rig := newBridgeRig(t, "normal", pluginToken, pluginToken)
	if rig.client == nil {
		t.Fatal(rig.err)
	}
	events := openPluginEvents(t, rig.base)

	var started StartOutput
	call(t, rig.client, "start_simulation", startArgs(), &started)
	st := events.waitFor(t, "started", nil)
	if st.Env.SessionID != started.SessionID || st.Env.Origin != session.OriginAgent {
		t.Fatalf("the plugin saw %s", st.Raw)
	}
}

func TestAttach_MidCallCancelReachesTheDaemon(t *testing.T) {
	rig := newBridgeRig(t, "normal", "", "")
	if rig.client == nil {
		t.Fatal(rig.err)
	}
	call(t, rig.client, "start_simulation", startArgs(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := rig.client.CallTool(ctx, &sdk.CallToolParams{Name: "get_live_telemetry", Arguments: map[string]any{"wait_s": 30}})
		errc <- err
	}()
	time.Sleep(400 * time.Millisecond)
	cancelledAt := time.Now()
	cancel()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("the cancelled call must fail on the client")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client call did not return after cancel")
	}

	deadline := time.After(cancelGrace - 300*time.Millisecond)
	for {
		if len(rig.rec.find(http.MethodPost, "notifications/cancelled")) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the cancel notification never reached the daemon")
		case <-time.After(20 * time.Millisecond):
		}
	}
	// The daemon, not the bridge's grace timer, must have ended the 30 s wait.
	for {
		var waiting []seen
		for _, c := range rig.rec.find(http.MethodPost, "tools/call") {
			if !c.Done.IsZero() && !c.Done.Before(cancelledAt) {
				waiting = append(waiting, c)
			}
		}
		if len(waiting) > 0 {
			if d := waiting[0].Done.Sub(cancelledAt); d > cancelGrace-200*time.Millisecond {
				t.Fatalf("the daemon kept waiting %s after the cancel", d)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("the daemon never finished the cancelled call")
		case <-time.After(20 * time.Millisecond):
		}
	}

	var tel session.Telemetry
	if res := call(t, rig.client, "get_live_telemetry", map[string]any{}, &tel); res.IsError || tel.State != session.Running {
		t.Errorf("the bridge must keep working after a cancel: %v %+v", errText(res), tel)
	}
}

func TestAttach_DaemonErrorsSurfaceInsteadOfHanging(t *testing.T) {
	rig := newBridgeRig(t, "normal", pluginToken, "wrong-token-0123456789")
	if rig.client != nil {
		t.Fatal("a wrong token must fail the handshake")
	}
	if rig.err == nil || !strings.Contains(rig.err.Error(), "401") {
		t.Errorf("error = %v; the HTTP status should reach the client", rig.err)
	}
}

// fakeDaemon is a minimal Streamable HTTP daemon for exercising the bridge's wire handling.
type fakeDaemon struct {
	rec     *recorder
	sse     bool // answer tools/call as an event stream
	failure int  // when set, tools/call gets this HTTP status
	push    chan string
}

func (s *fakeDaemon) handler() http.Handler {
	return s.rec.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			for {
				select {
				case msg := <-s.push:
					_, _ = io.WriteString(w, "id: 7\nevent: message\ndata: "+msg+"\n\n")
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		case http.MethodDelete:
			w.WriteHeader(204)
			return
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		switch m.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set(sessionHeader, "sess-1")
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(m.ID)+`,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"stub","version":"1"}}}`)
		case "tools/call":
			if s.failure != 0 {
				http.Error(w, "stub failure", s.failure)
				return
			}
			if s.sse {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
				_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\r\ndata: \"id\":"+string(m.ID)+",\"result\":{\"ok\":true}}\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "{\"jsonrpc\":\"2.0\",\n\"id\":"+string(m.ID)+",\"result\":{\"ok\":true}}")
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
}

type rawBridge struct {
	in    *io.PipeWriter
	lines chan string
	done  chan error
}

func newRawBridge(t *testing.T, endpoint string) *rawBridge {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	rb := &rawBridge{in: inW, lines: make(chan string, 64), done: make(chan error, 1)}
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			rb.lines <- sc.Text()
		}
	}()
	go func() {
		err := Attach(context.Background(), inR, outW, AttachOptions{Endpoint: endpoint})
		_ = outW.Close()
		rb.done <- err
	}()
	t.Cleanup(func() { _ = inW.Close() })
	return rb
}

func (rb *rawBridge) send(s string) { _, _ = io.WriteString(rb.in, s+"\n") }

func (rb *rawBridge) next(t *testing.T) string {
	t.Helper()
	select {
	case l := <-rb.lines:
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("no line from the bridge")
		return ""
	}
}

const initLine = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`

func startFakeDaemon(t *testing.T, s *fakeDaemon) string {
	t.Helper()
	s.rec = &recorder{}
	s.push = make(chan string, 4)
	ts := newLocalServer(t, s.handler())
	return ts
}

func newLocalServer(t *testing.T, h http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { _ = hs.Close() })
	return "http://" + ln.Addr().String() + "/mcp"
}

func TestAttach_WireHandling(t *testing.T) {
	for _, mode := range []string{"json", "sse"} {
		t.Run(mode, func(t *testing.T) {
			s := &fakeDaemon{sse: mode == "sse"}
			rb := newRawBridge(t, startFakeDaemon(t, s))

			rb.send(initLine)
			var init map[string]any
			_ = json.Unmarshal([]byte(rb.next(t)), &init)
			if init["id"] != float64(1) || init["result"] == nil {
				t.Fatalf("initialize answer = %v", init)
			}
			rb.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

			// A server-initiated message arrives on the standalone stream.
			deadline := time.After(5 * time.Second)
			for len(s.rec.find(http.MethodGet, "")) == 0 {
				select {
				case <-deadline:
					t.Fatal("the standalone stream was never opened")
				case <-time.After(10 * time.Millisecond):
				}
			}
			s.push <- `{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`
			if got := rb.next(t); got != `{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}` {
				t.Errorf("pushed message = %q", got)
			}

			rb.send(`{"jsonrpc":"2.0","id":"abc","method":"tools/call","params":{}}`)
			var lines []string
			for {
				l := rb.next(t)
				lines = append(lines, l)
				if strings.Contains(l, `"id":"abc"`) {
					break
				}
			}
			last := lines[len(lines)-1]
			if strings.Contains(last, "\n") || !json.Valid([]byte(last)) || !strings.Contains(last, `"ok":true`) {
				t.Errorf("response line = %q (one JSON message per line, even from multi-line or multi-data events)", last)
			}
			if mode == "sse" && len(lines) != 2 {
				t.Errorf("the progress notification before the answer must be forwarded: %v", lines)
			}

			calls := s.rec.find(http.MethodPost, "tools/call")
			if len(calls) != 1 || calls[0].Session != "sess-1" || calls[0].Protocol != "2025-06-18" {
				t.Errorf("headers on the call = %+v", calls)
			}
			if gets := s.rec.find(http.MethodGet, ""); gets[0].Session != "sess-1" || gets[0].Protocol != "2025-06-18" {
				t.Errorf("headers on the stream = %+v", gets)
			}

			_ = rb.in.Close()
			select {
			case <-rb.done:
			case <-time.After(5 * time.Second):
				t.Fatal("did not return after EOF")
			}
			if len(s.rec.find(http.MethodDelete, "")) != 1 {
				t.Error("the session should be deleted at the end")
			}
		})
	}
}

func TestAttach_FailedRequestsAreAnsweredNotDropped(t *testing.T) {
	s := &fakeDaemon{failure: http.StatusInternalServerError}
	rb := newRawBridge(t, startFakeDaemon(t, s))
	rb.send(initLine)
	rb.next(t)

	rb.send(`{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{}}`)
	var resp struct {
		ID    int `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(rb.next(t)), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 42 || resp.Error.Code == 0 || !strings.Contains(resp.Error.Message, "500") {
		t.Errorf("answer = %+v", resp)
	}

	// A notification that fails has no id to answer; nothing must be written.
	s.failure = http.StatusInternalServerError
	rb.send(`{"jsonrpc":"2.0","method":"tools/call","params":{}}`)
	select {
	case l := <-rb.lines:
		t.Errorf("a failed notification produced output: %q", l)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestAttach_UnreachableDaemonAnswersTheHandshake(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	rb := newRawBridge(t, "http://"+addr+EndpointPath)
	rb.send(initLine)
	if got := rb.next(t); !strings.Contains(got, `"error"`) || !strings.Contains(got, `"id":1`) {
		t.Errorf("answer = %q", got)
	}
}

func TestReadSSE(t *testing.T) {
	stream := ": comment\r\n" +
		"id: 1\r\nevent: message\r\ndata: {\"a\":\r\ndata: 1}\r\n\r\n" +
		"id: 2\ndata:{\"b\":2}\n\n" +
		"data: {\"tail\":true}" // no blank line before EOF
	var got []string
	var last string
	if err := readSSE(strings.NewReader(stream), &last, func(d []byte) { got = append(got, string(d)) }); err != nil {
		t.Fatal(err)
	}
	want := []string{"{\"a\":\n1}", `{"b":2}`, `{"tail":true}`}
	if len(got) != len(want) {
		t.Fatalf("events = %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
	if last != "2" {
		t.Errorf("last id = %q", last)
	}
}
