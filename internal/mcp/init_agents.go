package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Targets of `gg mcp init --ide`.
const (
	IDEVSCode    = "vscode"
	IDEJetBrains = "jetbrains"
	IDEClaude    = "claude"
	IDEAll       = "all"
)

// ACPAgentName is the key gg registers under in JetBrains' acp.json.
const ACPAgentName = "GopherGlide Autopilot"

// InitOptions say what `gg mcp init` writes.
type InitOptions struct {
	// Dir is the workspace root.
	Dir string
	// IDE is vscode, jetbrains, claude or all.
	IDE string
	// GG is the command the config files launch; default "gg".
	GG string
	// Force replaces files and entries that differ from gg's.
	Force bool
	// DryRun reports what would change and writes nothing.
	DryRun bool
}

// InitAction is the outcome for one file.
type InitAction struct {
	Path   string
	Status string // created, added, unchanged, replaced, skipped or would-*
	// Added is the text that was (or would be) written, for display.
	Added string
	Note  string
}

type workspaceFile struct {
	rel string
	// entry makes the file from one JSON server entry, merged into existing JSON config files.
	jsonKey  string
	jsonName string
	content  func(gg string) []byte
	merge    bool
}

func (o InitOptions) ggCommand() string {
	if o.GG != "" {
		return o.GG
	}
	return "gg"
}

func mcpServerEntry(gg string) []byte {
	b, _ := json.Marshal(map[string]any{"command": gg, "args": []string{"mcp"}})
	return b
}

func vscodeServerEntry(gg string) []byte {
	b, _ := json.Marshal(map[string]any{"type": "stdio", "command": gg, "args": []string{"mcp"}})
	return b
}

func filesFor(ide string) ([]workspaceFile, error) {
	mcpJSON := workspaceFile{rel: ".mcp.json", jsonKey: "mcpServers", jsonName: "gg", merge: true,
		content: mcpServerEntry}
	vscodeJSON := workspaceFile{rel: filepath.Join(".vscode", "mcp.json"), jsonKey: "servers", jsonName: "gg", merge: true,
		content: vscodeServerEntry}
	skill := workspaceFile{rel: filepath.Join(".claude", "skills", "gg", "SKILL.md"),
		content: func(string) []byte { return []byte(skillMarkdown()) }}
	var agents []workspaceFile
	for _, a := range agentFiles {
		a := a
		agents = append(agents, workspaceFile{rel: filepath.Join(".github", "agents", a.file),
			content: func(string) []byte { return []byte(a.render()) }})
	}
	switch ide {
	case IDEVSCode:
		return append([]workspaceFile{vscodeJSON}, agents...), nil
	case IDEJetBrains:
		return []workspaceFile{mcpJSON}, nil
	case IDEClaude:
		return []workspaceFile{mcpJSON, skill}, nil
	case IDEAll, "":
		return append([]workspaceFile{mcpJSON, vscodeJSON, skill}, agents...), nil
	}
	return nil, fmt.Errorf("unknown --ide %q (use vscode, jetbrains, claude or all)", ide)
}

// InitWorkspace writes the MCP config, skill and agent files for the chosen IDEs
// under o.Dir. It never overwrites a file that differs unless o.Force is set,
// and running it twice changes nothing.
func InitWorkspace(o InitOptions) ([]InitAction, error) {
	files, err := filesFor(o.IDE)
	if err != nil {
		return nil, err
	}
	if o.Dir == "" {
		o.Dir = "."
	}
	var out []InitAction
	for _, f := range files {
		path := filepath.Join(o.Dir, f.rel)
		var act InitAction
		if f.merge {
			act, err = initJSONFile(path, f, o)
		} else {
			act, err = initTextFile(path, f.content(o.ggCommand()), o)
		}
		if err != nil {
			return out, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, act)
	}
	return out, nil
}

func initTextFile(path string, want []byte, o InitOptions) (InitAction, error) {
	have, err := os.ReadFile(path)
	switch {
	case err == nil && bytes.Equal(have, want):
		return InitAction{Path: path, Status: "unchanged"}, nil
	case err == nil && !o.Force:
		return InitAction{Path: path, Status: "skipped", Note: "exists and differs; use --force to replace it"}, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return InitAction{}, err
	}
	status := "created"
	if err == nil {
		status = "replaced"
	}
	act := InitAction{Path: path, Status: status, Added: string(want)}
	if o.DryRun {
		act.Status = "would-" + status
		return act, nil
	}
	return act, writeWorkspaceFile(path, want)
}

func initJSONFile(path string, f workspaceFile, o InitOptions) (InitAction, error) {
	have, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return InitAction{}, err
	}
	entry := f.content(o.ggCommand())
	out, status, err := upsertEntry(have, f.jsonKey, f.jsonName, entry, o.Force)
	if err != nil {
		return InitAction{}, fmt.Errorf("existing file %w; fix or remove it first", err)
	}
	act := InitAction{Path: path, Status: status, Added: string(indentJSON(entry, "    "))}
	switch status {
	case spliceUnchanged:
		act.Added = ""
		return act, nil
	case spliceDiffers:
		act.Status = "skipped"
		act.Added = ""
		act.Note = fmt.Sprintf("has a different %q entry; use --force to replace it", f.jsonName)
		return act, nil
	}
	if o.DryRun {
		act.Status = "would-" + status
		return act, nil
	}
	return act, writeWorkspaceFile(path, out)
}

func writeWorkspaceFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ACPRegistration is the entry added to JetBrains' user-global acp.json.
type ACPRegistration struct {
	// Path is the acp.json to edit.
	Path string
	// GG is the absolute path of the gg binary.
	GG     string
	Force  bool
	DryRun bool
}

// DefaultACPPath is ~/.jetbrains/acp.json (%USERPROFILE%\.jetbrains\acp.json on Windows).
func DefaultACPPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".jetbrains", "acp.json"), nil
}

// ACPEntry is the JSON gg registers: an absolute command, since the IDE does not use the shell's PATH.
func ACPEntry(gg string) []byte {
	b, _ := json.Marshal(struct {
		Command      string   `json:"command"`
		Args         []string `json:"args"`
		UseCustomMCP bool     `json:"use_custom_mcp"`
	}{gg, []string{"autopilot", "--acp"}, true})
	return b
}

// RegisterACP adds gg to acp.json, keeping every other byte of the file. It
// refuses a file that is not valid JSON, and a different existing gg entry unless Force is set.
func RegisterACP(r ACPRegistration) (InitAction, error) {
	if !filepath.IsAbs(r.GG) {
		return InitAction{}, fmt.Errorf("the gg path must be absolute for acp.json, got %q", r.GG)
	}
	have, err := os.ReadFile(r.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return InitAction{}, err
	}
	entry := ACPEntry(r.GG)
	out, status, err := upsertEntry(have, "agent_servers", ACPAgentName, entry, r.Force)
	if err != nil {
		return InitAction{}, fmt.Errorf("%s %w; fix or move it, nothing was written", r.Path, err)
	}
	act := InitAction{Path: r.Path, Status: status, Added: ACPAgentName + ": " + string(indentJSON(entry, "    "))}
	switch status {
	case spliceUnchanged:
		act.Added = ""
		return act, nil
	case spliceDiffers:
		act.Status, act.Added = "skipped", ""
		act.Note = fmt.Sprintf("already has a different %q entry; use --force to replace it", ACPAgentName)
		return act, nil
	}
	act.Note = "restart the IDE to pick it up"
	if r.DryRun {
		act.Status = "would-" + status
		return act, nil
	}
	return act, writeWorkspaceFile(r.Path, out)
}

type agentFile struct {
	file        string
	description string
	body        string
}

func (a agentFile) render() string {
	return "---\ndescription: " + a.description + "\ntools: ['gg/*']\n---\n\n" + strings.TrimSpace(a.body) + "\n"
}

var agentFiles = []agentFile{
	{
		file:        "gg-journey.agent.md",
		description: "Write and verify a gg load-test file (.http) for this project's API",
		body: `You write gg load-test journeys. Read the gg://docs/http-syntax resource first.

1. Find the API's routes in this workspace (route files, OpenAPI spec) or ask for the base URL.
2. Write a .http file: one request per endpoint, chaining requests with @gg-export when a later call needs a value from an earlier response.
3. Call validate_http_file and fix every error.
4. Call run_journey_dry_run and fix any step that is not 2xx, using the status and body preview.
5. Save the file in the workspace only once every step passes.
The dry run sends real requests, so do not point it at data that must not change. Do not start a simulation.`,
	},
	{
		file:        "gg-pilot.agent.md",
		description: "Run a gg load test toward a goal, steer it and report the result",
		body: `You run gg load tests from a goal such as "find the ceiling" or "hold p99 under 200 ms at peak".

1. Validate the .http file. Choose the schedule: start_simulation with autopilot {mode: "find-ceiling"} and no profile for capacity questions; a profile with autopilot {mode: "slo-guard"} to defend an SLO; otherwise a profile from gg://profiles/catalog. Always set snap: true.
2. Watch with get_live_telemetry using wait_s and wake_on_p99_ms / wake_on_error_rate. Judge health on the window figures. Use steer_traffic only when the built-in autopilot is not running, and mark moments that explain what you did.
3. Call stop_simulation with report "md" when done, then report the goal, headline numbers, what degraded first (error_mix.top_endpoints), the autopilot outcome and the report path.
Never leave a simulation running.`,
	},
	{
		file:        "gg-triage.agent.md",
		description: "Compare gg snapshots and name the likely root causes of a regression",
		body: `You are a forensic analyst for gg snapshots. This agent only reads: it never starts or steers a simulation.

Use snap_list to find snapshots (newest first), snap_diff to compare a baseline with a current one and snap_assert for the verdict. Correlate latency and error changes with the timeline marks and steering that snap_diff reports, and with the per-endpoint error mix; say so when steering confounds the comparison. End with a ranked root-cause list: endpoint, what changed, by how much, and the most likely reason.`,
	},
}

// skillMarkdown is the Claude Code / Cursor skill for gg's tools and resources.
func skillMarkdown() string {
	tools := append([]string(nil), toolNames...)
	sort.Strings(tools)
	var b strings.Builder
	b.WriteString("---\nname: gg\ndescription: Load-test an HTTP API with gg (Gopher Glide): write and verify .http journeys, run and steer simulations, find the capacity ceiling, and compare snapshots for regressions. Use when asked to load test, stress test, benchmark an endpoint or check for performance regressions.\n---\n\n")
	b.WriteString("# gg\n\nGopher Glide's `gg mcp` server must be configured (see `.mcp.json`). Its tools:\n\n")
	for _, t := range tools {
		b.WriteString("- `" + t + "`\n")
	}
	b.WriteString("\nResources to read before you act:\n\n")
	b.WriteString("- `" + URIHTTPSyntax + "`: the .http file syntax; read it before writing a file\n")
	b.WriteString("- `" + URIConfigSchema + "`: the YAML config schema\n")
	b.WriteString("- `" + URIProfileCatalog + "`: built-in traffic profiles\n")
	b.WriteString(`
## Workflow

1. **Author**: write a .http file, fix every ` + "`validate_http_file`" + ` error, then prove it with ` + "`run_journey_dry_run`" + `. The dry run sends real requests.
2. **Run**: ` + "`start_simulation`" + ` returns a session_id at once. For "how much can it take" pass ` + "`autopilot {mode: \"find-ceiling\"}`" + ` with no profile; to defend an SLO pass a profile and ` + "`autopilot {mode: \"slo-guard\"}`" + `. Set ` + "`snap: true`" + ` to keep a snapshot.
3. **Watch**: ` + "`get_live_telemetry`" + ` with ` + "`wait_s`" + ` and ` + "`wake_on_p99_ms`" + ` / ` + "`wake_on_error_rate`" + ` instead of polling. Judge health on the window figures, not the cumulative ones.
4. **Steer**: ` + "`steer_traffic`" + ` (adjust_rps, set_rps, hold_rps, release_hold, mark). Not while the built-in autopilot owns the rate.
5. **Finish**: ` + "`stop_simulation`" + ` (add ` + "`report: \"md\"`" + ` for a shareable report), then ` + "`snap_list`" + `, ` + "`snap_diff`" + ` and ` + "`snap_assert`" + ` to compare against a baseline.

Never leave a simulation running, and do not aim a dry run or load at systems you do not own.
`)
	return b.String()
}
