package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// AttachOptions configure Attach.
type AttachOptions struct {
	// Endpoint is the daemon's Streamable HTTP URL, e.g. http://127.0.0.1:5000/mcp.
	Endpoint string
	// Token is sent as a bearer token when set.
	Token  string
	Logger *slog.Logger
	// HTTPClient is used for every request; nil means a default client.
	HTTPClient *http.Client
}

const (
	// drainGrace is how long in-flight calls may finish after the client closes stdin.
	drainGrace = 3 * time.Second
	// cancelGrace is how long a cancelled call gets to be ended by the daemon
	// before the bridge drops its own request.
	cancelGrace    = 2 * time.Second
	maxListenTries = 5
	maxEventBytes  = 16 << 20
)

const (
	sessionHeader  = "Mcp-Session-Id"
	protocolHeader = "MCP-Protocol-Version"
)

// Attach connects a stdio MCP client (in, out) to a running daemon's Streamable
// HTTP endpoint, so the client shares the daemon's sessions instead of
// spawning its own. It forwards whole JSON-RPC messages in both directions:
// a request may be answered over an event stream, server-initiated messages
// arrive on a separate GET stream, and the session and protocol version
// headers are carried once the handshake has set them. It returns when in
// reaches EOF or ctx ends.
func Attach(ctx context.Context, in io.Reader, out io.Writer, o AttachOptions) error {
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{}
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b := &bridge{ctx: ctx, o: o, out: out, inflight: map[string]context.CancelFunc{}}
	return b.run(in)
}

type bridge struct {
	ctx context.Context
	o   AttachOptions

	outMu sync.Mutex
	out   io.Writer

	wg       sync.WaitGroup
	listen   sync.Once
	mu       sync.Mutex
	session  string
	protocol string
	inflight map[string]context.CancelFunc // by JSON-RPC id, to drop a cancelled call
}

func (b *bridge) run(in io.Reader) error {
	r := bufio.NewReaderSize(in, 64<<10)
	var readErr error
	for {
		line, err := r.ReadBytes('\n')
		if msg := bytes.TrimSpace(line); len(msg) > 0 {
			b.wg.Add(1)
			go func(msg []byte) {
				defer b.wg.Done()
				b.forward(msg)
			}(append([]byte(nil), msg...))
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
	}

	done := make(chan struct{})
	go func() { b.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(drainGrace):
	case <-b.ctx.Done():
	}
	b.terminate()
	return readErr
}

// message is the part of a JSON-RPC message the bridge looks at.
type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		RequestID json.RawMessage `json:"requestId"`
	} `json:"params"`
}

func (m message) isRequest() bool { return m.Method != "" && len(m.ID) > 0 && string(m.ID) != "null" }

// forward posts one client message and relays whatever comes back.
func (b *bridge) forward(raw []byte) {
	var m message
	_ = json.Unmarshal(raw, &m)

	ctx, cancel := context.WithCancel(b.ctx)
	defer cancel()
	if m.isRequest() {
		key := string(m.ID)
		b.mu.Lock()
		b.inflight[key] = cancel
		b.mu.Unlock()
		defer func() {
			b.mu.Lock()
			delete(b.inflight, key)
			b.mu.Unlock()
		}()
	}
	if m.Method == "notifications/cancelled" && len(m.Params.RequestID) > 0 {
		b.dropAfterGrace(string(m.Params.RequestID))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.o.Endpoint, bytes.NewReader(raw))
	if err != nil {
		b.fail(m, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	b.addHeaders(req)

	resp, err := b.o.HTTPClient.Do(req)
	if err != nil {
		b.fail(m, err)
		return
	}
	defer resp.Body.Close()

	if m.Method == "initialize" && resp.StatusCode == http.StatusOK {
		b.mu.Lock()
		b.session = resp.Header.Get(sessionHeader)
		b.mu.Unlock()
	}
	switch {
	case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent:
		if m.Method == "notifications/initialized" {
			b.listen.Do(func() { go b.listenLoop() })
		}
	case resp.StatusCode >= 400:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		b.fail(m, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
	case strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"):
		if err := readSSE(resp.Body, nil, func(data []byte) { b.deliver(data, m.Method == "initialize") }); err != nil && ctx.Err() == nil {
			b.o.Logger.Warn("response stream ended early", "method", m.Method, "error", err)
		}
	default:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxEventBytes))
		if err != nil {
			b.fail(m, err)
			return
		}
		b.deliver(body, m.Method == "initialize")
	}
}

// dropAfterGrace ends the bridge's own request for a cancelled call if the
// daemon has not by then; a well-behaved daemon ends it first.
func (b *bridge) dropAfterGrace(id string) {
	time.AfterFunc(cancelGrace, func() {
		b.mu.Lock()
		cancel := b.inflight[id]
		b.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
}

func (b *bridge) addHeaders(req *http.Request) {
	if b.o.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.o.Token)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session != "" {
		req.Header.Set(sessionHeader, b.session)
	}
	if b.protocol != "" {
		req.Header.Set(protocolHeader, b.protocol)
	}
}

// deliver writes one message to the client as a single line. After the
// handshake it also remembers the negotiated protocol version.
func (b *bridge) deliver(data []byte, handshake bool) {
	var line bytes.Buffer
	if err := json.Compact(&line, data); err != nil {
		b.o.Logger.Warn("dropping a message that is not JSON", "error", err)
		return
	}
	if handshake {
		var res struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}
		if json.Unmarshal(line.Bytes(), &res) == nil && res.Result.ProtocolVersion != "" {
			b.mu.Lock()
			b.protocol = res.Result.ProtocolVersion
			b.mu.Unlock()
		}
	}
	line.WriteByte('\n')
	b.outMu.Lock()
	defer b.outMu.Unlock()
	if _, err := b.out.Write(line.Bytes()); err != nil {
		b.o.Logger.Warn("write to client failed", "error", err)
	}
}

// fail answers a request the daemon could not, so the client does not wait forever.
func (b *bridge) fail(m message, err error) {
	if b.ctx.Err() != nil {
		return
	}
	if !m.isRequest() {
		b.o.Logger.Warn("daemon request failed", "method", m.Method, "error", err)
		return
	}
	resp, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": m.ID,
		"error": map[string]any{"code": -32603, "message": "gg mcp daemon: " + err.Error()},
	})
	b.deliver(resp, false)
}

// listenLoop holds the standalone GET stream for server-initiated messages,
// resuming after a drop until the daemon stops answering.
func (b *bridge) listenLoop() {
	var lastID string
	for tries := 0; tries < maxListenTries && b.ctx.Err() == nil; tries++ {
		req, err := http.NewRequestWithContext(b.ctx, http.MethodGet, b.o.Endpoint, nil)
		if err != nil {
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		b.addHeaders(req)
		resp, err := b.o.HTTPClient.Do(req)
		if err == nil && resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return // 405: this daemon has no standalone stream
			}
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if err == nil {
			got := false
			_ = readSSE(resp.Body, &lastID, func(data []byte) { got = true; b.deliver(data, false) })
			_ = resp.Body.Close()
			if got {
				tries = 0
			}
		}
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(time.Duration(tries+1) * 300 * time.Millisecond):
		}
	}
}

// terminate ends the daemon-side MCP session, best effort.
func (b *bridge) terminate() {
	b.mu.Lock()
	id := b.session
	b.mu.Unlock()
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, b.o.Endpoint, nil)
	if err != nil {
		return
	}
	b.addHeaders(req)
	if resp, err := b.o.HTTPClient.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

// readSSE feeds each event's data to emit until the stream ends. When lastID
// is not nil it tracks the newest event id.
func readSSE(r io.Reader, lastID *string, emit func([]byte)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var data []byte
	flush := func() {
		if len(data) > 0 {
			emit(data)
		}
		data = nil
	}
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "" && err == nil:
			flush()
		case strings.HasPrefix(line, "data:"):
			if len(data)+len(line) > maxEventBytes {
				return errors.New("event too large")
			}
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")...)
		case strings.HasPrefix(line, "id:") && lastID != nil:
			*lastID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
		if err != nil {
			flush()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
