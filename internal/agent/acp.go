package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/coder/acp-go-sdk"
	"google.golang.org/adk/v2/model"

	ggmcp "github.com/shyam-s00/gopher-glide/internal/mcp"
)

// ACPOptions configure the Agent Client Protocol server.
type ACPOptions struct {
	Version string
	Model   ModelConfig
	Logger  *slog.Logger
	// MCPURL and MCPToken point at a running gg mcp daemon (the IDE plugin's);
	// when empty, an MCP server named "gg" given by the client is used, else gg runs in-process.
	MCPURL   string
	MCPToken string
	// Limits bound the in-process server.
	Limits ggmcp.Limits
	// NewBackend replaces backend creation; used by tests.
	NewBackend func(BackendOptions) (*Backend, error)
	// NewModel replaces model creation; used by tests.
	NewModel func(context.Context, ModelConfig) (model.LLM, error)
}

// ServeACP speaks ACP (protocol v1) as an agent over in/out until the client disconnects or ctx ends.
func ServeACP(ctx context.Context, in io.Reader, out io.Writer, o ACPOptions) error {
	a := newACPAgent(o)
	conn := acp.NewAgentSideConnection(a, out, in)
	a.conn = conn
	close(a.ready)
	select {
	case <-conn.Done():
	case <-ctx.Done():
	}
	a.shutdown()
	return nil
}

type acpAgent struct {
	opts   ACPOptions
	logger *slog.Logger
	// conn is set once, before ready closes; handlers wait on ready first.
	conn  *acp.AgentSideConnection
	ready chan struct{}

	mu       sync.Mutex
	sessions map[string]*acpSession
	shared   *Backend
	llm      model.LLM
	llmErr   error
	llmTried bool
}

type acpSession struct {
	id      acp.SessionId
	cwd     string
	backend *Backend

	mu     sync.Mutex
	cancel context.CancelFunc
	cmds   []acp.AvailableCommand
}

var _ acp.Agent = (*acpAgent)(nil)

func newACPAgent(o ACPOptions) *acpAgent {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.NewBackend == nil {
		o.NewBackend = NewBackend
	}
	if o.NewModel == nil {
		o.NewModel = NewModel
	}
	o.Model = ModelConfigFromEnv(o.Model)
	return &acpAgent{opts: o, logger: o.Logger, sessions: map[string]*acpSession{}, ready: make(chan struct{})}
}

func (a *acpAgent) shutdown() {
	a.mu.Lock()
	sessions := a.sessions
	a.sessions = map[string]*acpSession{}
	shared := a.shared
	a.shared = nil
	a.mu.Unlock()
	for _, s := range sessions {
		s.stopTurn()
		if s.backend != nil && s.backend != shared {
			s.backend.Close()
		}
	}
	if shared != nil {
		shared.Close()
	}
}

func (a *acpAgent) Initialize(_ context.Context, p acp.InitializeRequest) (acp.InitializeResponse, error) {
	title := "GopherGlide Autopilot"
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentInfo:         &acp.Implementation{Name: "gg-autopilot", Title: &title, Version: a.opts.Version},
		AgentCapabilities: acp.AgentCapabilities{},
		AuthMethods:       []acp.AuthMethod{},
	}, nil
}

func (a *acpAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *acpAgent) NewSession(ctx context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	<-a.ready
	be, err := a.backendFor(p.McpServers)
	if err != nil {
		return acp.NewSessionResponse{}, acp.NewInternalError(map[string]any{"error": err.Error()})
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	s := &acpSession{id: acp.SessionId("gg-" + hex.EncodeToString(raw[:])), cwd: p.Cwd, backend: be}
	a.mu.Lock()
	a.sessions[string(s.id)] = s
	a.mu.Unlock()

	// Sent after the response is on the wire; clients ignore updates for a session they do not know yet.
	go a.pushCommands(s)
	return acp.NewSessionResponse{SessionId: s.id}, nil
}

// backendFor picks the tools the session talks to.
func (a *acpAgent) backendFor(servers []acp.McpServer) (*Backend, error) {
	url, token := a.opts.MCPURL, a.opts.MCPToken
	if url == "" {
		url, token = ggServerFrom(servers)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if url == "" {
		if a.shared == nil {
			b, err := a.opts.NewBackend(BackendOptions{Limits: a.opts.Limits, Version: a.opts.Version, Logger: a.logger})
			if err != nil {
				return nil, err
			}
			a.shared = b
		}
		return a.shared, nil
	}
	return a.opts.NewBackend(BackendOptions{URL: url, Token: token, Logger: a.logger})
}

// ggServerFrom finds an HTTP MCP server called "gg" in what the client passed.
func ggServerFrom(servers []acp.McpServer) (url, token string) {
	for _, s := range servers {
		h := s.Http
		if h == nil || h.Name != "gg" {
			continue
		}
		for _, hd := range h.Headers {
			if strings.EqualFold(hd.Name, "authorization") {
				token = strings.TrimPrefix(hd.Value, "Bearer ")
			}
		}
		return h.Url, token
	}
	return "", ""
}

func (a *acpAgent) session(id acp.SessionId) (*acpSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[string(id)]
	if !ok {
		return nil, acp.NewInvalidParams(map[string]any{"error": "unknown session " + string(id)})
	}
	return s, nil
}

func (a *acpAgent) Cancel(_ context.Context, p acp.CancelNotification) error {
	if s, err := a.session(p.SessionId); err == nil {
		s.stopTurn()
	}
	return nil
}

func (s *acpSession) stopTurn() {
	s.mu.Lock()
	c := s.cancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

func (a *acpAgent) model(ctx context.Context) (model.LLM, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.llmTried {
		a.llmTried = true
		if HasModel(a.opts.Model) {
			a.llm, a.llmErr = a.opts.NewModel(ctx, a.opts.Model)
		} else {
			a.llmErr = ErrNoModel
		}
	}
	return a.llm, a.llmErr
}

func (a *acpAgent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	<-a.ready
	s, err := a.session(p.SessionId)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	turn, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return acp.PromptResponse{}, acp.NewInvalidParams(map[string]any{"error": "a turn is already running in this session"})
	}
	s.cancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
	}()

	text := promptText(p.Prompt)
	send := a.sender(turn, s)
	var runErr error
	if name, args, ok := ParseSlash(text); ok {
		runErr = a.runSlash(turn, s, name, args, send)
	} else {
		runErr = a.runFreeText(turn, s, text, send)
	}
	if turn.Err() != nil {
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	if runErr != nil {
		send(Event{Kind: KindText, Text: "**" + runErr.Error() + "**"})
	}
	go a.pushCommands(s)
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func promptText(blocks []acp.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Text != nil {
			parts = append(parts, b.Text.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func (a *acpAgent) runSlash(ctx context.Context, s *acpSession, name string, args Args, send Sink) error {
	if name == "generate" {
		return a.runGenerate(ctx, s, args, send)
	}
	if !IsCommand(name) {
		send(Event{Kind: KindText, Text: "Unknown command `/" + name + "`.\n\n" + a.help()})
		return nil
	}
	summary, err := RunCommand(ctx, s.backend, name, args.Resolve(s.cwd), send)
	if err != nil {
		return err
	}
	send(Event{Kind: KindText, Text: summary})
	return nil
}

func (a *acpAgent) runGenerate(ctx context.Context, s *acpSession, args Args, send Sink) error {
	llm, err := a.model(ctx)
	if err != nil {
		return err
	}
	task := strings.Join(args.Pos, " ")
	if task == "" {
		return errors.New("usage: /generate <what the API does, or a path to its OpenAPI spec>")
	}
	out := filepath.Join(s.cwd, "requests.http")
	_, err = Run(ctx, Options{Model: llm, Backend: s.backend, OutPath: out, Stream: true}, ModeGenerate, withCwd(s.cwd, task), send)
	if err == nil {
		send(Event{Kind: KindText, Text: "\n\nSaved " + out})
	}
	return err
}

func (a *acpAgent) runFreeText(ctx context.Context, s *acpSession, text string, send Sink) error {
	if text == "" {
		return nil
	}
	llm, err := a.model(ctx)
	if err != nil {
		send(Event{Kind: KindText, Text: "Open-ended requests need an LLM. Set `GEMINI_API_KEY` (or `GG_AUTOPILOT_BASE_URL` and `GG_AUTOPILOT_MODEL` for an OpenAI-compatible server) in the agent's environment.\n\nThe commands below work without one.\n\n" + a.help()})
		return nil
	}
	res, err := Run(ctx, Options{Model: llm, Backend: s.backend, Stream: true}, ModeRun, withCwd(s.cwd, text), send)
	if err == nil && res.Text == "" {
		send(Event{Kind: KindText, Text: "The model returned no answer."})
	}
	return err
}

func withCwd(cwd, task string) string {
	if cwd == "" {
		return task
	}
	return "Working directory: " + cwd + " (relative paths are relative to it)\n\n" + task
}

func (a *acpAgent) help() string {
	var b strings.Builder
	for _, c := range Commands {
		fmt.Fprintf(&b, "- `/%s %s`: %s\n", c.Name, c.Hint, c.Description)
	}
	return b.String()
}

// Resolve makes file arguments absolute against cwd.
func (a Args) Resolve(cwd string) Args {
	if cwd == "" {
		return a
	}
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(cwd, p)
	}
	out := Args{Named: map[string]string{}}
	for k, v := range a.Named {
		out.Named[k] = v
	}
	if v := out.Named["file"]; v != "" {
		out.Named["file"] = abs(v)
	}
	for _, k := range []string{"baseline", "current"} {
		if v := out.Named[k]; v != "" && looksLikePath(v) {
			out.Named[k] = abs(v)
		}
	}
	for _, p := range a.Pos {
		if looksLikePath(p) {
			p = abs(p)
		}
		out.Pos = append(out.Pos, p)
	}
	return out
}

func looksLikePath(s string) bool {
	return strings.ContainsAny(s, "/\\") || strings.HasSuffix(s, ".http") || strings.HasSuffix(s, ".snap")
}

// sender returns a Sink that streams events to the client as session/update notifications.
func (a *acpAgent) sender(ctx context.Context, s *acpSession) Sink {
	m := &updateMapper{}
	return func(ev Event) {
		for _, u := range m.updates(ev) {
			if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: s.id, Update: u}); err != nil && ctx.Err() == nil {
				a.logger.Warn("could not send session update", "error", err)
			}
		}
	}
}

// updateMapper turns Events into ACP updates, pairing tool results with their calls.
type updateMapper struct {
	n       int
	pending map[string][]acp.ToolCallId
}

func (m *updateMapper) updates(ev Event) []acp.SessionUpdate {
	switch ev.Kind {
	case KindThought:
		return []acp.SessionUpdate{acp.UpdateAgentThoughtText(ev.Text)}
	case KindText:
		return []acp.SessionUpdate{acp.UpdateAgentMessageText(ev.Text)}
	case KindPlan:
		entries := make([]acp.PlanEntry, len(ev.Plan))
		for i, st := range ev.Plan {
			entries[i] = acp.PlanEntry{Content: st.Title, Status: acp.PlanEntryStatus(st.Status), Priority: acp.PlanEntryPriorityMedium}
		}
		return []acp.SessionUpdate{acp.UpdatePlan(entries...)}
	case KindToolCall:
		id := m.newID(ev)
		return []acp.SessionUpdate{acp.StartToolCall(id, toolTitle(ev.Tool, ev.Args),
			acp.WithStartKind(toolKind(ev.Tool)), acp.WithStartStatus(acp.ToolCallStatusInProgress), acp.WithStartRawInput(ev.Args))}
	case KindToolResult:
		id, ok := m.take(ev)
		if !ok {
			return nil
		}
		status := acp.ToolCallStatusCompleted
		if _, failed := ev.Result["error"]; failed {
			status = acp.ToolCallStatusFailed
		}
		return []acp.SessionUpdate{acp.UpdateToolCall(id, acp.WithUpdateStatus(status), acp.WithUpdateRawOutput(ev.Result))}
	}
	return nil
}

func (m *updateMapper) newID(ev Event) acp.ToolCallId {
	m.n++
	id := acp.ToolCallId(ev.ToolID)
	if id == "" {
		id = acp.ToolCallId(fmt.Sprintf("call-%d", m.n))
	}
	if m.pending == nil {
		m.pending = map[string][]acp.ToolCallId{}
	}
	m.pending[ev.Tool] = append(m.pending[ev.Tool], id)
	return id
}

func (m *updateMapper) take(ev Event) (acp.ToolCallId, bool) {
	q := m.pending[ev.Tool]
	if ev.ToolID != "" {
		for i, id := range q {
			if string(id) == ev.ToolID {
				m.pending[ev.Tool] = append(q[:i:i], q[i+1:]...)
				return id, true
			}
		}
	}
	if len(q) == 0 {
		return "", false
	}
	m.pending[ev.Tool] = q[1:]
	return q[0], true
}

func toolTitle(name string, args map[string]any) string {
	if act, _ := args["action"].(string); act != "" {
		return name + " " + act
	}
	return name
}

func toolKind(name string) acp.ToolKind {
	switch name {
	case "start_simulation", "stop_simulation", "steer_traffic", "run_journey_dry_run":
		return acp.ToolKindExecute
	case "get_live_telemetry", "snap_list", "snap_capture", "snap_diff", "snap_assert":
		return acp.ToolKindRead
	}
	return acp.ToolKindOther
}

// commandsNow lists the commands worth offering: /generate needs an LLM, /triage needs two snapshots.
func (a *acpAgent) commandsNow(ctx context.Context, s *acpSession) []acp.AvailableCommand {
	snaps := a.snapshotCount(ctx, s)
	var out []acp.AvailableCommand
	for _, c := range Commands {
		if c.Name == "triage" && snaps < 2 {
			continue
		}
		out = append(out, availableCommand(c))
	}
	if HasModel(a.opts.Model) {
		out = append(out, availableCommand(Command{Name: "generate", Description: "Write and verify a .http file for an API (needs an LLM)", Hint: "describe the API or give an OpenAPI path"}))
	}
	return out
}

func availableCommand(c Command) acp.AvailableCommand {
	return acp.AvailableCommand{Name: c.Name, Description: c.Description,
		Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: c.Hint}}}
}

func (a *acpAgent) snapshotCount(ctx context.Context, s *acpSession) int {
	cs, err := s.backend.Client(ctx)
	if err != nil {
		return 0
	}
	defer func() { _ = cs.Close() }()
	var list ggmcp.ListOutput
	if err := callTool(ctx, cs, "snap_list", map[string]any{"limit": 2}, &list); err != nil {
		return 0
	}
	return list.Total
}

// pushCommands sends the command list, and again whenever it changes.
func (a *acpAgent) pushCommands(s *acpSession) {
	ctx := context.Background()
	cmds := a.commandsNow(ctx, s)
	s.mu.Lock()
	same := sameCommands(s.cmds, cmds)
	if !same {
		s.cmds = cmds
	}
	s.mu.Unlock()
	if same {
		return
	}
	u := acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
		AvailableCommands: cmds, SessionUpdate: "available_commands_update"}}
	if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: s.id, Update: u}); err != nil {
		a.logger.Warn("could not advertise commands", "error", err)
	}
}

func sameCommands(a, b []acp.AvailableCommand) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

func notSupported(method string) *acp.RequestError { return acp.NewMethodNotFound(method) }

func (a *acpAgent) CloseSession(_ context.Context, p acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	a.mu.Lock()
	s := a.sessions[string(p.SessionId)]
	delete(a.sessions, string(p.SessionId))
	a.mu.Unlock()
	if s != nil {
		s.stopTurn()
	}
	return acp.CloseSessionResponse{}, nil
}

func (a *acpAgent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, notSupported(acp.AgentMethodSessionList)
}

func (a *acpAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, notSupported(acp.AgentMethodLogout)
}

func (a *acpAgent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, notSupported(acp.AgentMethodSessionResume)
}

func (a *acpAgent) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, notSupported(acp.AgentMethodSessionSetConfigOption)
}

func (a *acpAgent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, notSupported(acp.AgentMethodSessionSetMode)
}
