package controlhttp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeStream struct {
	ch     chan Event
	closed chan struct{}
	err    error // returned once ch is closed
}

func (f *fakeStream) Close() { close(f.closed) }

func (f *fakeStream) Next(ctx context.Context) (Event, error) {
	select {
	case ev, ok := <-f.ch:
		if !ok {
			if f.err != nil {
				return Event{}, f.err
			}
			return Event{}, io.EOF
		}
		return ev, nil
	case <-ctx.Done():
		return Event{}, ctx.Err()
	}
}

type fakeTarget struct {
	stream *fakeStream
	reply  []byte
	err    error
	got    []byte
}

func (f *fakeTarget) Subscribe() (Stream, error) { return f.stream, nil }

func (f *fakeTarget) Command(_ context.Context, body []byte) ([]byte, error) {
	f.got = body
	return f.reply, f.err
}

func newTarget() *fakeTarget {
	return &fakeTarget{stream: &fakeStream{ch: make(chan Event, 8), closed: make(chan struct{})}}
}

func TestControl_PassesTheBodyAndReturnsTheReplyUnchanged(t *testing.T) {
	ft := newTarget()
	ft.reply = []byte(`{"event":"ack","id":"a","command":"bias","bias":5}`)
	rec := httptest.NewRecorder()
	Control(ft).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"command":"bias","amount":5}`)))

	if rec.Code != 200 || rec.Body.String() != string(ft.reply) || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if string(ft.got) != `{"command":"bias","amount":5}` {
		t.Errorf("target got %q", ft.got)
	}
}

func TestControl_ErrorsBecomeLCPErrorEvents(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		reason string
	}{
		{"typed", &Error{Status: 404, Reason: "unknown_session", Message: "no such run"}, 404, "unknown_session"},
		{"wrapped", errors.Join(errors.New("ctx"), &Error{Status: 409, Reason: "session_not_running", Message: "over"}), 409, "session_not_running"},
		{"plain", errors.New("boom"), 500, "internal"},
	}
	for _, c := range cases {
		ft := newTarget()
		ft.err = c.err
		rec := httptest.NewRecorder()
		Control(ft).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"event":"error"`) || !strings.Contains(rec.Body.String(), c.reason) {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
}

func TestControl_RefusesOtherMethodsAndHugeBodies(t *testing.T) {
	rec := httptest.NewRecorder()
	Control(newTarget()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET: %d %v", rec.Code, rec.Header())
	}

	rec = httptest.NewRecorder()
	big := strings.NewReader(`{"label":"` + strings.Repeat("x", MaxBodyBytes) + `"}`)
	Control(newTarget()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", big))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("big body: %d", rec.Code)
	}
}

func TestEvents_WritesSSEAndEndsWhenTheStreamDoes(t *testing.T) {
	ft := newTarget()
	ft.stream.ch <- Event{Name: "started", Data: []byte(`{"a":1}`)}
	ft.stream.ch <- Event{Name: "bad\nname", Data: []byte(`{"b":2}`)}
	close(ft.stream.ch)

	srv := httptest.NewServer(Events(ft))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("headers = %v", resp.Header)
	}
	want := ": connected\n\nevent: started\ndata: {\"a\":1}\n\nevent: badname\ndata: {\"b\":2}\n\n"
	if string(body) != want {
		t.Errorf("body = %q\nwant   %q", body, want)
	}
	select {
	case <-ft.stream.closed:
	case <-time.After(time.Second):
		t.Error("the stream must be closed when the handler returns")
	}
}

func TestEvents_ClientGoneClosesTheStream(t *testing.T) {
	ft := newTarget()
	srv := httptest.NewServer(Events(ft))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = bufio.NewReader(resp.Body).ReadString('\n')
	cancel()
	_ = resp.Body.Close()
	select {
	case <-ft.stream.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("subscription leaked after the client left")
	}
}

func TestEvents_IdleStreamSendsKeepAlives(t *testing.T) {
	old := KeepAlive
	KeepAlive = 20 * time.Millisecond
	defer func() { KeepAlive = old }()

	ft := newTarget()
	srv := httptest.NewServer(Events(ft))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	var pings int
	deadline := time.After(3 * time.Second)
	for pings < 3 {
		line := make(chan string, 1)
		go func() { s, _ := r.ReadString('\n'); line <- s }()
		select {
		case s := <-line:
			if strings.HasPrefix(s, ": ping") {
				pings++
			}
		case <-deadline:
			t.Fatalf("only %d keep-alives", pings)
		}
	}
}

func TestEvents_StreamFailureIsReportedBeforeClosing(t *testing.T) {
	ft := newTarget()
	ft.stream.err = errors.New("too slow")
	close(ft.stream.ch)
	rec := httptest.NewRecorder()
	Events(ft).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "event: error") || !strings.Contains(rec.Body.String(), "too slow") {
		t.Errorf("body = %q", rec.Body)
	}
}

func TestEvents_RefusesOtherMethods(t *testing.T) {
	rec := httptest.NewRecorder()
	Events(newTarget()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}
