package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
	"github.com/shyam-s00/gopher-glide/internal/tui"
	"github.com/shyam-s00/gopher-glide/internal/version"
)

func main() {
	fmt.Printf("gg (Gopher Glide) %s (commit:%s) built %s\n",
		version.Version, version.GitCommit, version.GetBuildDate())

	if len(os.Args) < 2 {
		fmt.Println("Usage: gg <config-file> [--snap] [--snap-tag TAG] [--snap-dir DIR] [--snap-sample RATE]")
		fmt.Println("       gg snap <list|view> [--snap-dir DIR]")
		os.Exit(1)
	}

	// ── snap subcommand router ────────────────────────────────────────────────
	// Dispatched before the config-load so `gg snap` works without a config file.
	if os.Args[1] == "snap" {
		runSnapCmd(os.Args[2:])
		return
	}

	configPath := os.Args[1]

	// ── snap flags ────────────────────────────────────────────────────────────
	// Parsed from os.Args[2:] so <config-file> always stays as the first arg.
	fs := flag.NewFlagSet("gg", flag.ExitOnError)
	snapEnabled := fs.Bool("snap", false, "capture a behavioral snapshot after the run")
	snapTag := fs.String("snap-tag", "", "tag to attach to the snapshot (e.g. v1.2.0-pre)")
	snapDir := fs.String("snap-dir", "", "override the default snapshot directory")
	snapSample := fs.Float64("snap-sample", 0.05, "fraction of responses to sample for schema inference (0.0–1.0)")
	_ = fs.Parse(os.Args[2:])

	// Any snap-specific flag passed explicitly implicitly enables snapping,
	// so users don't need to type both --snap and e.g. --snap-dir.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "snap-dir", "snap-tag", "snap-sample":
			*snapEnabled = true
		}
	})

	// ── load config ───────────────────────────────────────────────────────────
	cfg, err := config.Load(configPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Configuration loaded successfully\n")
	fmt.Printf("  HttpFile: %s\n", cfg.ConfigSection.HTTPFilePath)
	fmt.Printf("  Prometheus: %t\n", cfg.ConfigSection.Prometheus)
	fmt.Printf("  Stages: %d stage(s)\n", len(cfg.Stages))
	for i, s := range cfg.Stages {
		fmt.Printf("    [%d] duration=%s targetRPS=%d\n", i+1, s.Duration, s.TargetRPS)
	}

	// ── parse .http file ──────────────────────────────────────────────────────
	specs, err := httpreader.ParseFile(cfg.ConfigSection.HTTPFilePath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error parsing http file: %v\n", err)
		os.Exit(1)
	}
	if len(specs) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "No requests found in http file: %s\n", cfg.ConfigSection.HTTPFile)
		os.Exit(1)
	}

	fmt.Printf("✓ Loaded %d request(s) from %s\n", len(specs), cfg.ConfigSection.HTTPFile)
	for i, s := range specs {
		fmt.Printf("  [%d] %s %s\n", i+1, s.Method, s.URL)
	}

	// ── set up recorder (optional) ────────────────────────────────────────────
	var rec *snap.DefaultRecorder
	var resolvedSnapDir string

	if *snapEnabled {
		resolvedSnapDir, err = snap.EnsureSnapDir(*snapDir)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Error setting up snap directory: %v\n", err)
			os.Exit(1)
		}
		rec = snap.NewDefaultRecorder(0)
		fmt.Printf("📸 Snapping → %s\n", resolvedSnapDir)
	}

	// ── build engine ──────────────────────────────────────────────────────────
	var engineOpts []engine.EngineOption
	if rec != nil {
		engineOpts = append(engineOpts,
			engine.WithRecorder(rec),
			engine.WithSampleRate(*snapSample),
		)
	}
	eng := engine.New(engineOpts...)

	// ── start TUI ─────────────────────────────────────────────────────────────
	// onRunComplete is dispatched by the TUI as a background goroutine once
	// the engine finishes all stages naturally. It must not write to
	// stdout/stderr (alt-screen is still active). The returned string is
	// displayed in the director bar.
	var snapDone atomic.Bool

	var onRunComplete func() string
	if rec != nil {
		onRunComplete = func() string {
			if !snapDone.CompareAndSwap(false, true) {
				return "" // already handled
			}
			status, err := finalizeSnapResult(rec, eng, cfg, *snapTag, resolvedSnapDir)
			if err != nil {
				return fmt.Sprintf("⚠  snap error: %v", err)
			}
			return status
		}
	}

	fmt.Println("Starting TUI...")
	if err := tui.Start(eng, cfg, specs, *snapEnabled, resolvedSnapDir, onRunComplete); err != nil {
		fmt.Printf("Error running TUI: %v\n", err)
		os.Exit(1)
	}

	// ── early-quit fallback ───────────────────────────────────────────────────
	// Reached when the user presses [q] before all stages completed, meaning
	// onRunComplete was never called by the TUI.  The CAS ensures we don't
	// double-finalise when the run is completed AND the user then pressed [q].
	if rec != nil && snapDone.CompareAndSwap(false, true) {
		finalizeSnap(rec, eng, cfg, *snapTag, resolvedSnapDir)
	}
}

// finalizeSnapResult drains the recorder, aggregates stats, and writes the
// .snap file to dir. Returns a human-readable status line on success.
// It does not write to stdout or stderr, making it safe to call while the
// TUI alt-screen is active.
func finalizeSnapResult(rec *snap.DefaultRecorder, eng *engine.Engine, cfg *config.Config, tag, dir string) (string, error) {
	endTime := eng.GetEndTime()
	if endTime.IsZero() {
		endTime = time.Now()
	}

	snapData, err := rec.Finalize(snap.RunMeta{
		Tag:       tag,
		Config:    cfg,
		StartTime: eng.GetStartTime(),
		EndTime:   endTime,
		PeakRPS:   cfg.PeakRPS(),
	})
	if err != nil {
		return "", fmt.Errorf("finalize recorder: %w", err)
	}

	filename := snap.FileName(tag, endTime)
	outPath := filepath.Join(dir, filename)

	if err := snap.Write(snapData, outPath); err != nil {
		return "", fmt.Errorf("write snapshot: %w", err)
	}

	status := fmt.Sprintf("✓ Snapshot saved → %s", outPath)
	if dropped := rec.Dropped(); dropped > 0 {
		status += fmt.Sprintf("  ⚠ %d dropped", dropped)
	}
	return status, nil
}

// finalizeSnap is the early-quit path: TUI has already exited so printing
// to stdout/stderr is safe.
func finalizeSnap(rec *snap.DefaultRecorder, eng *engine.Engine, cfg *config.Config, tag, dir string) {
	fmt.Println("Finalizing snapshot...")
	status, err := finalizeSnapResult(rec, eng, cfg, tag, dir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	fmt.Println(status)
}

// ── snap subcommand handlers ──────────────────────────────────────────────────

func runSnapCmd(args []string) {
	if len(args) == 0 {
		snapUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "list":
		runSnapList(args[1:])
	case "view":
		runSnapView(args[1:])
	default:
		_, err := fmt.Fprintf(os.Stderr, "unknown snap subcommand %q\n\n", args[0])
		if err != nil {
			// error writing to stderr, exiting
		}
		snapUsage()
		os.Exit(1)
	}
}

func runSnapList(args []string) {
	fs := flag.NewFlagSet("gg snap list", flag.ExitOnError)
	snapDir := fs.String("snap-dir", "", "override the default snapshot directory")
	_ = fs.Parse(args)

	dir, err := snap.ResolveSnapDir(*snapDir)
	if err != nil {
		_, err := fmt.Fprintf(os.Stderr, "snap list: resolve directory: %v\n", err)
		if err != nil {
			return
		}
		os.Exit(1)
	}

	summaries, err := snap.ListAll(dir)
	if err != nil {
		_, err := fmt.Fprintf(os.Stderr, "snap list: %v\n", err)
		if err != nil {
			return
		}
		os.Exit(1)
	}
	if len(summaries) == 0 {
		fmt.Printf("No snapshots found in %s\n", dir)
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tTAG\tDATE\tREQUESTS\tPEAK RPS\tENDPOINTS")
	_, _ = fmt.Fprintln(w, "--\t---\t----\t--------\t--------\t---------")
	for _, s := range summaries {
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%d\n",
			s.ID,
			snapDisplayTag(s.Tag),
			s.Date.Format("2006-01-02 15:04"),
			snapFormatCount(s.TotalRequests),
			s.PeakRPS,
			s.EndpointCount,
		)
	}
	_ = w.Flush()
}

func runSnapView(args []string) {
	fs := flag.NewFlagSet("gg snap view", flag.ExitOnError)
	snapDir := fs.String("snap-dir", "", "override the default snapshot directory")

	// Go's flag package stops at the first non-flag argument, so
	// `gg snap view 2 --snap-dir /tmp` would leave --snap-dir unparsed.
	// Pull the positional target out first when it leads the arg list,
	// then parse the remainder as flags. The fallback (flags first, then
	// the target) is handled by fs.Args() after parsing.
	var target string
	var flagArgs []string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target = args[0]
		flagArgs = args[1:]
	} else {
		flagArgs = args
	}
	_ = fs.Parse(flagArgs)
	if target == "" {
		if rest := fs.Args(); len(rest) > 0 {
			target = rest[0]
		}
	}

	if target == "" {
		_, _ = fmt.Fprintln(os.Stderr, "Usage: gg snap view <id|tag|file> [--snap-dir DIR]")
		os.Exit(1)
	}

	dir, err := snap.ResolveSnapDir(*snapDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap view: resolve directory: %v\n", err)
		os.Exit(1)
	}

	var info *snap.SnapInfo
	if id, atoiErr := strconv.Atoi(target); atoiErr == nil {
		// numeric → look up by 1-based ID
		info, err = snap.FindByID(dir, id)
	} else if _, statErr := os.Stat(target); statErr == nil {
		// existing path → load directly
		si := snap.SnapInfo{Path: target, FileName: filepath.Base(target)}
		info = &si
	} else {
		// fall back to tag search
		info, err = snap.FindByTag(dir, target)
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap view: %v\n", err)
		os.Exit(1)
	}

	s, err := snap.Read(info.Path)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap view: read %q: %v\n", info.Path, err)
		os.Exit(1)
	}

	if err := tui.StartSnapViewer(s, *info); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap view: %v\n", err)
		os.Exit(1)
	}
}

func snapUsage() {
	fmt.Fprintln(os.Stderr, "Usage: gg snap <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  list                    [--snap-dir DIR]   list all saved snapshots")
	fmt.Fprintln(os.Stderr, "  view <id|tag|file>      [--snap-dir DIR]   view a single snapshot")
}

func snapDisplayTag(tag string) string {
	if tag == "" || tag == "run" {
		return "(untagged)"
	}
	return tag
}

func snapFormatCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%d,%03d,%03d", n/1_000_000, (n/1_000)%1_000, n%1_000)
	case n >= 1_000:
		return fmt.Sprintf("%d,%03d", n/1_000, n%1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
