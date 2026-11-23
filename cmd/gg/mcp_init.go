package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shyam-s00/gopher-glide/internal/mcp"
)

type mcpInitOptions struct {
	dir         string
	ide         string
	gg          string
	force       bool
	print       bool
	registerACP bool
	acpPath     string
}

func parseMCPInitFlags(args []string, out io.Writer) (mcpInitOptions, error) {
	fs := flag.NewFlagSet("gg mcp init", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		_, _ = fmt.Fprint(out, `Usage: gg mcp init [flags]

Write the files that connect an IDE agent to gg, in the current directory:
  vscode     .vscode/mcp.json and .github/agents/gg-{journey,pilot,triage}.agent.md (Copilot agent mode)
  jetbrains  .mcp.json
  claude     .mcp.json and .claude/skills/gg/SKILL.md (Claude Code, Cursor)
Existing files are never overwritten without --force, and running it twice changes nothing.

--register-acp also lists "gg autopilot" in the user-wide ~/.jetbrains/acp.json so JetBrains
can pick it as an agent. That file is shared by every project, so it is only touched when you ask.

Flags:
`)
		fs.PrintDefaults()
	}
	ide := fs.String("ide", mcp.IDEAll, "what to set up: vscode, jetbrains, claude or all")
	dir := fs.String("dir", ".", "workspace root to write into")
	gg := fs.String("gg-path", "", "gg command the config files run (default: gg if it is on PATH, else this binary)")
	force := fs.Bool("force", false, "replace files and entries that differ")
	pr := fs.Bool("print", false, "show what would be written, write nothing")
	acp := fs.Bool("register-acp", false, "also add gg to the user-wide JetBrains acp.json")
	acpPath := fs.String("acp-file", "", "acp.json to edit with --register-acp (default ~/.jetbrains/acp.json)")
	if err := fs.Parse(args); err != nil {
		return mcpInitOptions{}, err
	}
	if fs.NArg() > 0 {
		return mcpInitOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	switch *ide {
	case mcp.IDEAll, mcp.IDEVSCode, mcp.IDEJetBrains, mcp.IDEClaude:
	default:
		return mcpInitOptions{}, fmt.Errorf("unknown --ide %q (use vscode, jetbrains, claude or all)", *ide)
	}
	if *acpPath != "" && !*acp {
		return mcpInitOptions{}, errors.New("--acp-file only applies with --register-acp")
	}
	return mcpInitOptions{dir: *dir, ide: *ide, gg: *gg, force: *force, print: *pr, registerACP: *acp, acpPath: *acpPath}, nil
}

// ggCommandFor picks what config files should run: "gg" when the shell can find it, else this binary.
func ggCommandFor(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if _, err := lookPath("gg"); err == nil {
		return "gg", nil
	}
	return absoluteExe()
}

func absoluteExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate the gg binary (pass --gg-path): %w", err)
	}
	return filepath.Abs(exe)
}

func runMCPInit(args []string, out, errOut io.Writer) int {
	o, err := parseMCPInitFlags(args, errOut)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "gg mcp init: %v\n", err)
		return 2
	}
	gg, err := ggCommandFor(o.gg)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "gg mcp init: %v\n", err)
		return 1
	}
	acts, err := mcp.InitWorkspace(mcp.InitOptions{Dir: o.dir, IDE: o.ide, GG: gg, Force: o.force, DryRun: o.print})
	printInitActions(out, acts)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "gg mcp init: %v\n", err)
		return 1
	}

	if o.registerACP {
		path := o.acpPath
		if path == "" {
			if path, err = mcp.DefaultACPPath(); err != nil {
				_, _ = fmt.Fprintf(errOut, "gg mcp init: %v\n", err)
				return 1
			}
		}
		exe, err := absoluteExe()
		if o.gg != "" {
			exe, err = filepath.Abs(o.gg)
		}
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "gg mcp init: %v\n", err)
			return 1
		}
		act, err := mcp.RegisterACP(mcp.ACPRegistration{Path: path, GG: exe, Force: o.force, DryRun: o.print})
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "gg mcp init: %v\n", err)
			return 1
		}
		printInitActions(out, []mcp.InitAction{act})
	}
	return 0
}

func printInitActions(out io.Writer, acts []mcp.InitAction) {
	for _, a := range acts {
		_, _ = fmt.Fprintf(out, "%-14s %s\n", a.Status, a.Path)
		if a.Note != "" {
			_, _ = fmt.Fprintf(out, "               %s\n", a.Note)
		}
		if strings.HasPrefix(a.Status, "would-") || a.Status == "added" || a.Status == "created" || a.Status == "replaced" {
			for _, line := range strings.Split(strings.TrimRight(a.Added, "\n"), "\n") {
				if line != "" {
					_, _ = fmt.Fprintf(out, "  + %s\n", line)
				}
			}
		}
	}
}

var lookPath = exec.LookPath
