package mcp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/dryrun"
	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
	"github.com/shyam-s00/gopher-glide/internal/profile"
)

const (
	httpFileName   = "requests.http"
	configFileName = "gg.yaml"
	heartbeatEvery = "1s"
)

var snapTagPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// StartInput are the arguments of start_simulation.
type StartInput struct {
	HTTPContent string `json:"http_content,omitempty" jsonschema:"the .http file text. Give either http_content or http_path"`
	HTTPPath    string `json:"http_path,omitempty" jsonschema:"path of a .http file on the machine running gg. Give either http_content or http_path"`
	Profile     string `json:"profile,omitempty" jsonschema:"built-in traffic profile name from gg://profiles/catalog. Give either profile or config_yaml"`
	PeakRPS     int    `json:"peak_rps,omitempty" jsonschema:"peak requests per second when using a profile; default is the profile's own"`
	Duration    string `json:"duration,omitempty" jsonschema:"total run time when using a profile, e.g. 2m; default is the profile's own"`
	ConfigYAML  string `json:"config_yaml,omitempty" jsonschema:"full gg YAML config (see generate_config and gg://schema/gg-yaml); its httpFile is replaced by the supplied http file"`
	Snap        bool   `json:"snap,omitempty" jsonschema:"record a behavioural snapshot of the run, saved when it ends"`
	SnapTag     string `json:"snap_tag,omitempty" jsonschema:"label for the snapshot, letters digits . _ - only"`
}

// StageOut is one planned stage.
type StageOut struct {
	Name      string  `json:"name"`
	DurationS float64 `json:"duration_s"`
	TargetRPS int     `json:"target_rps"`
}

// StartOutput is the result of start_simulation.
type StartOutput struct {
	SessionID string        `json:"session_id"`
	State     session.State `json:"state"`
	PeakRPS   int           `json:"peak_rps"`
	DurationS float64       `json:"duration_s"`
	Stages    []StageOut    `json:"stages"`
	Snapshot  bool          `json:"snapshot"`
	Next      string        `json:"next"`
}

// SessionRef selects a simulation; it may be empty when exactly one exists.
type SessionRef struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"id returned by start_simulation; may be omitted when only one simulation exists"`
}

// TelemetryInput are the arguments of get_live_telemetry.
type TelemetryInput struct {
	SessionID    string  `json:"session_id,omitempty" jsonschema:"id returned by start_simulation; may be omitted when only one simulation exists"`
	WaitS        float64 `json:"wait_s,omitempty" jsonschema:"seconds to wait for a new reading before answering, 0 to 30; default 0 answers at once. The wait ends early if the run ends or a wake_on_* threshold is met"`
	WakeOnP99Ms  float64 `json:"wake_on_p99_ms,omitempty" jsonschema:"end the wait early when a new reading's windowed p99 latency (last few seconds) is at or above this many ms; needs wait_s"`
	WakeOnErrors float64 `json:"wake_on_error_rate,omitempty" jsonschema:"end the wait early when a new reading's windowed error rate (0-1, last few seconds) is at or above this; needs wait_s"`
	WakeOnAny    bool    `json:"wake_on_any_error,omitempty" jsonschema:"end the wait early when a new reading shows any failure in error_mix; needs wait_s"`
}

func (in TelemetryInput) watch() (session.Watch, error) {
	w := session.Watch{WakeP99Ms: in.WakeOnP99Ms, WakeErrRate: in.WakeOnErrors, WakeAnyError: in.WakeOnAny}
	switch {
	case math.IsNaN(in.WaitS) || in.WaitS < 0 || in.WaitS > session.MaxWait.Seconds():
		return w, fmt.Errorf("wait_s must be between 0 and %.0f", session.MaxWait.Seconds())
	case in.WakeOnP99Ms < 0 || math.IsNaN(in.WakeOnP99Ms):
		return w, errors.New("wake_on_p99_ms must be positive")
	case in.WakeOnErrors < 0 || in.WakeOnErrors > 1 || math.IsNaN(in.WakeOnErrors):
		return w, errors.New("wake_on_error_rate must be between 0 and 1")
	case in.WaitS == 0 && (in.WakeOnP99Ms != 0 || in.WakeOnErrors != 0 || in.WakeOnAny):
		return w, errors.New("wake_on_* conditions need wait_s greater than 0")
	}
	w.Wait = time.Duration(in.WaitS * float64(time.Second))
	return w, nil
}

func registerSimTools(s *sdk.Server, mgr *session.Manager, l Limits, exe string) {
	yes := true
	sdk.AddTool(s, &sdk.Tool{
		Name: "start_simulation",
		Description: "Start a real load test and return immediately with a session_id; the run continues in the background. " +
			"Needs a .http file (http_content or http_path) and a load shape: a built-in profile (optionally scaled with peak_rps and duration) or a full config_yaml. " +
			"The file is validated first (use validate_http_file and run_journey_dry_run beforehand). The server enforces caps on peak RPS, run time, concurrent simulations and target hosts, " +
			"and refuses with the flag that raises each. After starting, poll get_live_telemetry and finish with stop_simulation. " +
			"This sends real traffic to the target: only use hosts the user has agreed to load test.",
		Annotations: &sdk.ToolAnnotations{DestructiveHint: &yes, OpenWorldHint: &yes, Title: "Start simulation"},
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in StartInput) (*sdk.CallToolResult, StartOutput, error) {
		out, err := startSimulation(ctx, mgr, l, exe, in)
		return nil, out, err
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "get_live_telemetry",
		Description: "Read the current state of a simulation: state (starting, running, stopping, finished, stopped, failed), " +
			"elapsed_s since the run started, stage, target vs actual RPS, p50/p95/p99 latency in ms, error rate, request counts, and error_mix " +
			"(what kinds of failures happened in the last 10 s: transport, 4xx, 5xx, extraction failures, top status codes). " +
			"Numbers are the latest once-per-second reading (age_s says how old; two polls within a second can return the same reading); 'starting' means no reading yet. " +
			"metrics are cumulative since the start, but error_mix is a rolling 10-second window of recent failures, so its counts are not comparable to the totals. recent_marks and recent_steers list what steer_traffic already did. A finished run returns final statistics and snapshot_path; a failed one returns the reason and the child's stderr tail. " +
			"window is health over the last few seconds (p99, error rate, sample count), which shows a recovery or a new breach long before the cumulative numbers move. " +
			"hold_rps appears while the rate is pinned. " +
			"To watch without polling, set wait_s (up to 30): the call answers when the wait ends, when the run ends, or earlier if wake_on_p99_ms, wake_on_error_rate or wake_on_any_error is met by a reading that arrives during the wait; " +
			"window_summary then gives waited_s, wake_reason (timer, p99_threshold, error_threshold, run_ended) and the actual-RPS and p99 range seen.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, Title: "Live telemetry"},
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in TelemetryInput) (*sdk.CallToolResult, session.Telemetry, error) {
		watch, err := in.watch()
		if err != nil {
			return nil, session.Telemetry{}, err
		}
		sess, err := mgr.Resolve(in.SessionID)
		if err != nil {
			return nil, session.Telemetry{}, err
		}
		if watch.Wait <= 0 {
			return nil, sess.Telemetry(), nil
		}
		sum, err := sess.Wait(ctx, watch)
		if err != nil {
			return nil, session.Telemetry{}, err
		}
		t := sess.Telemetry()
		t.WindowSummary = &sum
		return nil, t, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "stop_simulation",
		Description: "Stop a simulation cleanly and return its final statistics. gg finishes writing the snapshot (snapshot_path, when snap was on) before exiting; " +
			"if the run had not reached its planned length, the result says it stopped early and that the statistics and snapshot cover only that part; " +
			"a run that does not stop in time is terminated. Safe to call again or on a run that already ended. Always stop what you started.",
		Annotations: &sdk.ToolAnnotations{IdempotentHint: true, Title: "Stop simulation"},
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in SessionRef) (*sdk.CallToolResult, session.Telemetry, error) {
		sess, err := mgr.Resolve(in.SessionID)
		if err != nil {
			return nil, session.Telemetry{}, err
		}
		sess.Stop(ctx, mgr.StopOptions())
		return nil, sess.Telemetry(), nil
	})

	registerSteerTool(s, mgr, l)
}

// plannedRun is a validated request, ready to launch.
type plannedRun struct {
	dir     string
	args    []string
	peakRPS int
	dur     time.Duration
}

func startSimulation(ctx context.Context, mgr *session.Manager, l Limits, exe string, in StartInput) (StartOutput, error) {
	run, err := planRun(l, in)
	if err != nil {
		return StartOutput{}, err
	}
	sess, err := mgr.Start(ctx, session.Spec{Exe: exe, Args: run.args, Dir: run.dir, Planned: run.dur, PeakRPS: run.peakRPS})
	if err != nil {
		return StartOutput{}, err
	}

	out := StartOutput{
		SessionID: sess.ID, State: sess.State(), PeakRPS: run.peakRPS, DurationS: run.dur.Seconds(), Snapshot: in.Snap,
		Next: "Poll get_live_telemetry (the first numbers arrive within about a second) and call stop_simulation when done.",
	}
	stages, _, _, _ := sess.Started()
	for _, st := range stages {
		out.Stages = append(out.Stages, StageOut{Name: st.Name, DurationS: st.DurationSeconds, TargetRPS: st.TargetRPS})
	}
	return out, nil
}

// planRun validates the request against the caps and writes the run's input
// files; nothing is spawned and, on error, nothing is left on disk.
func planRun(l Limits, in StartInput) (run plannedRun, err error) {
	content, err := HTTPSource{Content: in.HTTPContent, Path: in.HTTPPath}.load()
	if err != nil {
		return run, err
	}
	if err := checkShape(in); err != nil {
		return run, err
	}
	if in.SnapTag != "" && !snapTagPattern.MatchString(in.SnapTag) {
		return run, errors.New("snap_tag may only contain letters, digits, '.', '_' and '-' (max 64)")
	}
	if rep := dryrun.Validate(content); !rep.OK {
		return run, fmt.Errorf("the .http file has errors; fix them first (validate_http_file shows all): %s", firstIssues(rep.Errors, 3))
	}
	plan, err := dryrun.NewPlan(content)
	if err != nil {
		return run, err
	}
	if err := checkStaticHosts(plan, l, true); err != nil {
		return run, err
	}

	run.dir, err = os.MkdirTemp("", "gg-mcp-")
	if err != nil {
		return run, fmt.Errorf("create work dir: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(run.dir)
		}
	}()
	httpPath := filepath.Join(run.dir, httpFileName)
	if err := os.WriteFile(httpPath, []byte(content), 0o600); err != nil {
		return run, err
	}

	run.args = []string{}
	if in.ConfigYAML != "" {
		cfgPath, cfg, err := writeConfig(run.dir, in.ConfigYAML)
		if err != nil {
			return run, err
		}
		run.peakRPS, run.dur = cfg.PeakRPS(), cfg.TotalDuration()
		run.args = append(run.args, cfgPath)
	}
	profileArgs := []string(nil)
	if in.Profile != "" {
		profileArgs, run.peakRPS, run.dur, err = profileRun(in)
		if err != nil {
			return run, err
		}
	}
	if err := l.CheckRun(run.peakRPS, run.dur); err != nil {
		return run, err
	}

	run.args = append(run.args,
		"--headless", "--reporter", "json", "--heartbeat-interval", heartbeatEvery,
		"--control", "stdin", "--control-exit-on-eof", "--http-file", httpPath)
	run.args = append(run.args, profileArgs...)
	if in.Snap {
		run.args = append(run.args, "--snap")
		if in.SnapTag != "" {
			run.args = append(run.args, "--snap-tag", in.SnapTag)
		}
	} else if in.SnapTag != "" {
		return run, errors.New("snap_tag needs snap: true")
	}
	return run, nil
}

func checkShape(in StartInput) error {
	switch {
	case in.Profile != "" && in.ConfigYAML != "":
		return errors.New("give either profile or config_yaml, not both")
	case in.Profile == "" && in.ConfigYAML == "":
		return errors.New("give a load shape: a profile (see gg://profiles/catalog) or a config_yaml (see generate_config)")
	case in.Profile == "" && (in.PeakRPS != 0 || in.Duration != ""):
		return errors.New("peak_rps and duration apply to a profile; with config_yaml put them in the stages")
	}
	return nil
}

// profileRun resolves a built-in profile's effective peak and length and the
// flags that make gg run it.
func profileRun(in StartInput) (args []string, peak int, dur time.Duration, err error) {
	prof, err := profile.LoadBuiltIn(in.Profile)
	if err != nil || !profile.IsBuiltIn(in.Profile) {
		return nil, 0, 0, fmt.Errorf("unknown profile %q; see gg://profiles/catalog", in.Profile)
	}
	peak, dur = prof.DefaultPeakRPS, prof.DefaultDuration
	args = []string{"--profile", prof.Name}
	if in.PeakRPS < 0 {
		return nil, 0, 0, errors.New("peak_rps must be positive")
	}
	if in.PeakRPS > 0 {
		peak = in.PeakRPS
		args = append(args, "--peak-rps", strconv.Itoa(peak))
	}
	if in.Duration != "" {
		d, perr := time.ParseDuration(in.Duration)
		if perr != nil || d <= 0 {
			return nil, 0, 0, fmt.Errorf("duration %q is not a valid positive duration (use e.g. 90s, 5m)", in.Duration)
		}
		dur = d
		args = append(args, "--duration", d.String())
	}
	return args, peak, dur, nil
}

// writeConfig stores the YAML in dir with httpFile pointing at the supplied
// file, and loads it with the CLI's own loader so invalid configs fail here.
func writeConfig(dir, text string) (string, *config.Config, error) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil || doc == nil {
		return "", nil, fmt.Errorf("config_yaml is not valid YAML: %v", err)
	}
	section, _ := doc["config"].(map[string]any)
	if section == nil {
		section = map[string]any{}
	}
	section["httpFile"] = httpFileName
	doc["config"] = section
	b, err := yaml.Marshal(doc)
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, configFileName)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return "", nil, fmt.Errorf("config_yaml is invalid: %w", unwrapAll(err))
	}
	return path, cfg, nil
}

func unwrapAll(err error) error {
	for {
		inner := errors.Unwrap(err)
		if inner == nil {
			return err
		}
		err = inner
	}
}

func firstIssues(issues []dryrun.Issue, n int) string {
	var parts []string
	for i, is := range issues {
		if i == n {
			parts = append(parts, fmt.Sprintf("and %d more", len(issues)-n))
			break
		}
		parts = append(parts, fmt.Sprintf("line %d: %s", is.Line, is.Message))
	}
	return strings.Join(parts, "; ")
}
