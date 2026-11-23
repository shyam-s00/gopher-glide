package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// otherAgents is a hand-formatted acp.json with agents and keys gg must not disturb.
const otherAgents = `{
	"unknown_top": [1,   2, 3],
	"agent_servers": {
		"Claude Code":   { "command": "/usr/bin/claude", "args": ["--acp"],  "env": {"X": "1"} },
		"Gemini": {"command":"gemini","args":["--experimental-acp"]}
	},
	"trailing": true
}
`

func TestRegisterACP_KeepsEverythingElseByteForByte(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".jetbrains", "acp.json")
	write(t, path, otherAgents)

	act, err := RegisterACP(ACPRegistration{Path: path, GG: "/opt/gg/bin/gg"})
	if err != nil || act.Status != "added" {
		t.Fatalf("act=%+v err=%v", act, err)
	}
	got := read(t, path)
	if !json.Valid([]byte(got)) {
		t.Fatalf("result is not valid JSON:\n%s", got)
	}

	// Everything before the new member and after it is untouched.
	prefix := otherAgents[:strings.Index(otherAgents, "\n\t},\n\t\"trailing\"")]
	suffix := otherAgents[strings.Index(otherAgents, "\n\t},\n\t\"trailing\""):]
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, suffix) {
		t.Errorf("other bytes changed:\n%s", got)
	}

	var doc struct {
		Agents map[string]struct {
			Command      string   `json:"command"`
			Args         []string `json:"args"`
			UseCustomMCP bool     `json:"use_custom_mcp"`
		} `json:"agent_servers"`
	}
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}
	e := doc.Agents[ACPAgentName]
	if e.Command != "/opt/gg/bin/gg" || strings.Join(e.Args, " ") != "autopilot --acp" || !e.UseCustomMCP {
		t.Errorf("entry = %+v", e)
	}
	if len(doc.Agents) != 3 {
		t.Errorf("agents = %d, want the 2 originals plus gg", len(doc.Agents))
	}
}

func TestRegisterACP_SecondRunIsANoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acp.json")
	write(t, path, otherAgents)
	reg := ACPRegistration{Path: path, GG: "/opt/gg"}
	if _, err := RegisterACP(reg); err != nil {
		t.Fatal(err)
	}
	first := read(t, path)
	act, err := RegisterACP(reg)
	if err != nil || act.Status != "unchanged" || read(t, path) != first {
		t.Fatalf("act=%+v err=%v changed=%v", act, err, read(t, path) != first)
	}
}

func TestRegisterACP_DifferentEntryNeedsForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acp.json")
	write(t, path, otherAgents)
	if _, err := RegisterACP(ACPRegistration{Path: path, GG: "/old/gg"}); err != nil {
		t.Fatal(err)
	}
	before := read(t, path)
	act, err := RegisterACP(ACPRegistration{Path: path, GG: "/new/gg"})
	if err != nil || act.Status != "skipped" || read(t, path) != before {
		t.Fatalf("act=%+v err=%v", act, err)
	}
	act, err = RegisterACP(ACPRegistration{Path: path, GG: "/new/gg", Force: true})
	if err != nil || act.Status != "replaced" {
		t.Fatalf("act=%+v err=%v", act, err)
	}
	got := read(t, path)
	if !strings.Contains(got, "/new/gg") || strings.Contains(got, "/old/gg") || !strings.Contains(got, `"Claude Code":   { "command": "/usr/bin/claude"`) {
		t.Errorf("force result:\n%s", got)
	}
}

func TestRegisterACP_InvalidJSONIsRefusedAndUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acp.json")
	const broken = `{"agent_servers": {"a": }`
	write(t, path, broken)
	_, err := RegisterACP(ACPRegistration{Path: path, GG: "/gg"})
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v", err)
	}
	if read(t, path) != broken {
		t.Error("the file was modified")
	}
}

func TestRegisterACP_Shapes(t *testing.T) {
	cases := map[string]struct {
		in     string
		wantOK bool
	}{
		"missing file":        {"", true},
		"empty object":        {`{}`, true},
		"no agent_servers":    {`{"other": 1}`, true},
		"empty agent_servers": {`{"agent_servers": {}}`, true},
		"agent_servers array": {`{"agent_servers": []}`, false},
		"top level array":     {`[1]`, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "acp.json")
			if c.in != "" {
				write(t, path, c.in)
			}
			_, err := RegisterACP(ACPRegistration{Path: path, GG: "/gg"})
			if (err == nil) != c.wantOK {
				t.Fatalf("err = %v", err)
			}
			if c.wantOK {
				var v map[string]any
				if err := json.Unmarshal([]byte(read(t, path)), &v); err != nil {
					t.Fatalf("invalid result: %v\n%s", err, read(t, path))
				}
				if _, ok := v["agent_servers"].(map[string]any)[ACPAgentName]; !ok {
					t.Errorf("entry missing: %v", v)
				}
			} else if c.in != "" && read(t, path) != c.in {
				t.Error("a refused file was modified")
			}
		})
	}
}

func TestRegisterACP_RelativeCommandAndDryRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acp.json")
	if _, err := RegisterACP(ACPRegistration{Path: path, GG: "gg"}); err == nil {
		t.Error("a relative command was accepted")
	}
	act, err := RegisterACP(ACPRegistration{Path: path, GG: "/gg", DryRun: true})
	if err != nil || act.Status != "would-created" || act.Added == "" {
		t.Fatalf("act=%+v err=%v", act, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("dry run wrote the file")
	}
}

func TestInitWorkspace_CreatesFilesAndTwiceIsANoOp(t *testing.T) {
	dir := t.TempDir()
	acts, err := InitWorkspace(InitOptions{Dir: dir, IDE: IDEAll})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".mcp.json", ".vscode/mcp.json", ".claude/skills/gg/SKILL.md",
		".github/agents/gg-journey.agent.md", ".github/agents/gg-pilot.agent.md", ".github/agents/gg-triage.agent.md"}
	for _, rel := range want {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("%s not created", rel)
		}
	}
	for _, a := range acts {
		if a.Status != "created" {
			t.Errorf("%s: %s", a.Path, a.Status)
		}
	}
	snapshot := map[string]string{}
	for _, rel := range want {
		snapshot[rel] = read(t, filepath.Join(dir, rel))
	}
	acts, err = InitWorkspace(InitOptions{Dir: dir, IDE: IDEAll})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if a.Status != "unchanged" {
			t.Errorf("second run: %s %s", a.Path, a.Status)
		}
	}
	for _, rel := range want {
		if read(t, filepath.Join(dir, rel)) != snapshot[rel] {
			t.Errorf("%s changed on the second run", rel)
		}
	}

	var mcpCfg struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(snapshot[".mcp.json"]), &mcpCfg); err != nil || mcpCfg.Servers["gg"].Command != "gg" || mcpCfg.Servers["gg"].Args[0] != "mcp" {
		t.Errorf(".mcp.json = %s (%v)", snapshot[".mcp.json"], err)
	}
	if !strings.Contains(snapshot[".vscode/mcp.json"], `"servers"`) || !strings.Contains(snapshot[".vscode/mcp.json"], `"type": "stdio"`) {
		t.Errorf(".vscode/mcp.json = %s", snapshot[".vscode/mcp.json"])
	}
}

func TestInitWorkspace_MergesIntoAnExistingMCPConfig(t *testing.T) {
	dir := t.TempDir()
	const existing = "{\n  \"mcpServers\": {\n    \"github\": {\"command\": \"gh-mcp\"}\n  }\n}\n"
	write(t, filepath.Join(dir, ".mcp.json"), existing)
	acts, err := InitWorkspace(InitOptions{Dir: dir, IDE: IDEJetBrains})
	if err != nil || acts[0].Status != "added" {
		t.Fatalf("acts=%+v err=%v", acts, err)
	}
	got := read(t, filepath.Join(dir, ".mcp.json"))
	if !strings.Contains(got, `"github": {"command": "gh-mcp"}`) || !strings.Contains(got, `"gg"`) {
		t.Errorf("merge result:\n%s", got)
	}
}

func TestInitWorkspace_NeverOverwritesWithoutForce(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(dir, ".claude", "skills", "gg", "SKILL.md")
	write(t, skill, "my own notes\n")
	write(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"gg":{"command":"/custom/gg","args":["mcp","--max-rps","50"]}}}`)

	acts, err := InitWorkspace(InitOptions{Dir: dir, IDE: IDEClaude})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if a.Status != "skipped" || a.Note == "" {
			t.Errorf("%s: %s %q", a.Path, a.Status, a.Note)
		}
	}
	if read(t, skill) != "my own notes\n" || !strings.Contains(read(t, filepath.Join(dir, ".mcp.json")), "/custom/gg") {
		t.Error("an existing file was overwritten without --force")
	}

	if _, err := InitWorkspace(InitOptions{Dir: dir, IDE: IDEClaude, Force: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(t, skill), "my own notes") || strings.Contains(read(t, filepath.Join(dir, ".mcp.json")), "/custom/gg") {
		t.Error("--force did not replace")
	}
}

func TestInitWorkspace_DryRunWritesNothingAndBadInputFails(t *testing.T) {
	dir := t.TempDir()
	acts, err := InitWorkspace(InitOptions{Dir: dir, IDE: IDEAll, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if a.Status != "would-created" {
			t.Errorf("%s: %s", a.Path, a.Status)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("dry run created %d entries", len(entries))
	}
	if _, err := InitWorkspace(InitOptions{Dir: dir, IDE: "emacs"}); err == nil {
		t.Error("unknown ide accepted")
	}
	write(t, filepath.Join(dir, ".mcp.json"), "{broken")
	if _, err := InitWorkspace(InitOptions{Dir: dir, IDE: IDEJetBrains}); err == nil || read(t, filepath.Join(dir, ".mcp.json")) != "{broken" {
		t.Errorf("invalid existing config: err=%v", err)
	}
}

func TestInitWorkspace_CustomCommandIsUsed(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitWorkspace(InitOptions{Dir: dir, IDE: IDEJetBrains, GG: "/usr/local/bin/gg"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, filepath.Join(dir, ".mcp.json")), "/usr/local/bin/gg") {
		t.Error("--gg-path ignored")
	}
}

func TestSkillAndAgentsOnlyNameRealToolsAndResources(t *testing.T) {
	skill := skillMarkdown()
	for _, tool := range toolNames {
		if !strings.Contains(skill, "`"+tool+"`") {
			t.Errorf("SKILL.md omits tool %s", tool)
		}
	}
	for _, uri := range []string{URIHTTPSyntax, URIConfigSchema, URIProfileCatalog} {
		if !strings.Contains(skill, uri) {
			t.Errorf("SKILL.md omits resource %s", uri)
		}
	}
	known := map[string]bool{}
	for _, n := range toolNames {
		known[n] = true
	}
	for _, a := range agentFiles {
		text := a.render()
		if !strings.HasPrefix(text, "---\ndescription: ") || !strings.Contains(text, "tools: ['gg/*']") {
			t.Errorf("%s: bad front matter", a.file)
		}
		// Anything in the text that looks like a tool name must be one.
		for _, w := range strings.FieldsFunc(text, func(r rune) bool { return !(r == '_' || r >= 'a' && r <= 'z') }) {
			if (strings.HasPrefix(w, "snap_") || strings.HasSuffix(w, "_simulation") || strings.HasSuffix(w, "_traffic") ||
				strings.HasPrefix(w, "validate_") || strings.HasPrefix(w, "run_journey")) && !known[w] {
				t.Errorf("%s names a tool gg does not have: %s", a.file, w)
			}
		}
	}
	if !bytes.Contains([]byte(agentFiles[2].body), []byte("never starts or steers")) {
		t.Error("the triage agent must be read-only")
	}
}
