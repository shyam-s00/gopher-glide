package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/shyam-s00/gopher-glide/internal/controlhttp"
	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
)

// Paths of the IDE plugin bridge, served beside EndpointPath.
const (
	PluginEventsPath  = "/plugin/events"
	PluginControlPath = "/plugin/control"
	PluginStartPath   = "/plugin/start"
)

const maxPluginStartBytes = 4 << 20

// pluginTarget serves every session of the daemon to the IDE plugin.
type pluginTarget struct{ mgr *session.Manager }

type eventStream struct{ sub *session.Subscription }

func (e eventStream) Close() { e.sub.Close() }

func (e eventStream) Next(ctx context.Context) (controlhttp.Event, error) {
	ev, err := e.sub.Next(ctx)
	if err != nil {
		return controlhttp.Event{}, err
	}
	return controlhttp.Event{Name: ev.Name, Data: ev.JSON()}, nil
}

func (p pluginTarget) Subscribe() (controlhttp.Stream, error) {
	return eventStream{p.mgr.Subscribe()}, nil
}

// Command routes an LCP command to the session named in its session_id. The
// command is the protocol's own object; session_id is the only addition, and
// agent_steer is the one command the daemon answers itself.
func (p pluginTarget) Command(ctx context.Context, body []byte) ([]byte, error) {
	var cmd map[string]any
	if err := json.Unmarshal(body, &cmd); err != nil || cmd == nil {
		return nil, &controlhttp.Error{Status: http.StatusBadRequest, Reason: "invalid_argument", Message: "the body must be one JSON command object"}
	}
	id, _ := cmd["session_id"].(string)
	delete(cmd, "session_id")

	sess, err := p.mgr.ResolveFor(id, session.OriginUser)
	var nf *session.NotFoundError
	if errors.As(err, &nf) {
		return nil, &controlhttp.Error{Status: http.StatusNotFound, Reason: "unknown_session", Message: err.Error()}
	}
	if err != nil {
		return nil, err
	}

	if cmd["command"] == "agent_steer" {
		return agentSteer(sess, cmd)
	}
	raw, err := sess.Command(ctx, cmd, "user")
	var nr *session.NotRunningError
	switch {
	case errors.Is(err, session.ErrUnknownCommand):
		return nil, &controlhttp.Error{Status: http.StatusBadRequest, Reason: "unknown_command", Message: err.Error()}
	case errors.As(err, &nr):
		return nil, &controlhttp.Error{Status: http.StatusConflict, Reason: "session_not_running", Message: err.Error()}
	case err != nil:
		return nil, &controlhttp.Error{Status: http.StatusBadGateway, Reason: "no_reply", Message: err.Error()}
	}
	return raw, nil
}

func agentSteer(sess *session.Session, cmd map[string]any) ([]byte, error) {
	bad := func(msg string) error {
		return &controlhttp.Error{Status: http.StatusBadRequest, Reason: "invalid_argument", Message: msg}
	}
	allow, ok := cmd["allow"].(bool)
	switch {
	case !ok:
		return nil, bad("agent_steer needs allow: true or false")
	case sess.Origin() != session.OriginUser:
		return nil, bad("agent_steer applies to runs the user started; agent runs are always open to the agent")
	}
	sess.SetAgentSteer(allow)
	reply := map[string]any{"event": "ack", "command": "agent_steer", "allow": allow, "session_id": sess.ID}
	if id, ok := cmd["id"]; ok {
		reply["id"] = id
	}
	return json.Marshal(reply)
}

// PluginStartInput are the arguments of POST /plugin/start: start_simulation's,
// plus a config file to run as it is and the agent-steering switch.
type PluginStartInput struct {
	StartInput
	ConfigPath      string `json:"config_path,omitempty"`
	AllowAgentSteer bool   `json:"allow_agent_steer,omitempty"`
}

func (s *Server) pluginStart() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			pluginError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPluginStartBytes))
		if err != nil {
			pluginError(w, http.StatusRequestEntityTooLarge, "invalid_argument", "request body too large or unreadable")
			return
		}
		var in PluginStartInput
		if err := json.Unmarshal(body, &in); err != nil {
			pluginError(w, http.StatusBadRequest, "invalid_argument", "the body must be a JSON object of start arguments: "+err.Error())
			return
		}
		out, err := startRun(r.Context(), s.sessions, s.limits, s.exe, in.StartInput,
			runOpts{user: true, configPath: in.ConfigPath, allowAgentSteer: in.AllowAgentSteer})
		var le *session.LimitError
		var se *startError
		switch {
		case errors.As(err, &le):
			pluginError(w, http.StatusConflict, "session_limit", err.Error())
		case errors.As(err, &se):
			pluginError(w, http.StatusUnprocessableEntity, "start_failed", err.Error())
		case err != nil:
			pluginError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		}
	})
}

func pluginError(w http.ResponseWriter, status int, reason, msg string) {
	e := &controlhttp.Error{Status: status, Reason: reason, Message: msg}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(e.Body())
}
