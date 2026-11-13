// Package controlhttp serves the Live Control Protocol over HTTP: commands as
// POST requests and events as a Server-Sent Events stream. The handlers work
// against a Target, so one implementation backs both a multi-session daemon
// and a single run.
package controlhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// KeepAlive is how often an idle event stream sends a comment so proxies and
// clients can tell the connection is alive.
var KeepAlive = 15 * time.Second

// MaxBodyBytes bounds a command request.
const MaxBodyBytes = 64 << 10

// Event is one message of an event stream.
type Event struct {
	// Name is the SSE event name, normally the LCP event.
	Name string
	// Data is one line of JSON.
	Data []byte
}

// Stream is a source of events. Next returns io.EOF when the stream has ended
// for good, or ctx's error when ctx ends first.
type Stream interface {
	Next(ctx context.Context) (Event, error)
	Close()
}

// Target is what the handlers serve.
type Target interface {
	// Subscribe opens an event stream that starts with the current state.
	Subscribe() (Stream, error)
	// Command runs one command given as the JSON request body and returns
	// the reply event. Refusals that belong to the HTTP layer are *Error.
	Command(ctx context.Context, body []byte) ([]byte, error)
}

// Error is a request that failed before or instead of getting an LCP reply.
// It is written as an LCP "error" event with the HTTP status that fits.
type Error struct {
	Status  int
	Reason  string
	Message string
}

func (e *Error) Error() string { return e.Reason + ": " + e.Message }

// Body renders the error as an LCP error event.
func (e *Error) Body() []byte {
	b, _ := json.Marshal(map[string]string{"event": "error", "reason": e.Reason, "message": e.Message})
	return b
}

func writeError(w http.ResponseWriter, e *Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_, _ = w.Write(e.Body())
}

// Control serves commands: POST a JSON command, get the reply event back.
func Control(t Target) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, &Error{http.StatusMethodNotAllowed, "method_not_allowed", "use POST"})
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
		if err != nil {
			writeError(w, &Error{http.StatusRequestEntityTooLarge, "invalid_argument", "request body too large or unreadable"})
			return
		}
		reply, err := t.Command(r.Context(), body)
		var he *Error
		switch {
		case errors.As(err, &he):
			writeError(w, he)
		case err != nil:
			writeError(w, &Error{http.StatusInternalServerError, "internal", err.Error()})
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(reply)
		}
	})
}

// Events serves the event stream as Server-Sent Events.
func Events(t Target) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, &Error{http.StatusMethodNotAllowed, "method_not_allowed", "use GET"})
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, &Error{http.StatusInternalServerError, "internal", "streaming is not supported here"})
			return
		}
		st, err := t.Subscribe()
		if err != nil {
			writeError(w, &Error{http.StatusServiceUnavailable, "unavailable", err.Error()})
			return
		}
		defer st.Close()

		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": connected\n\n")
		flusher.Flush()

		for {
			ctx, cancel := context.WithTimeout(r.Context(), KeepAlive)
			ev, err := st.Next(ctx)
			cancel()
			switch {
			case err == nil:
				if writeEvent(w, ev) != nil {
					return
				}
			case errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil:
				if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
					return
				}
			case errors.Is(err, io.EOF), r.Context().Err() != nil:
				return
			default:
				_ = writeEvent(w, Event{Name: "error", Data: (&Error{Reason: "stream_closed", Message: err.Error()}).Body()})
				flusher.Flush()
				return
			}
			flusher.Flush()
		}
	})
}

func writeEvent(w io.Writer, ev Event) error {
	name := strings.NewReplacer("\n", "", "\r", "").Replace(ev.Name)
	_, err := io.WriteString(w, "event: "+name+"\ndata: "+string(ev.Data)+"\n\n")
	return err
}
