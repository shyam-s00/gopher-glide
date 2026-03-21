package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
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
		os.Exit(1)
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

	// Passing any snap-specific flag implicitly enables snapping.
	// Users shouldn't need to type both --snap and --snap-dir.
	if *snapDir != "" || *snapTag != "" {
		*snapEnabled = true
	}

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
	// onRunComplete is called by the TUI exactly once when the engine finishes
	// all stages naturally. A CAS guard ensures it also covers the early-quit
	// path (user presses [q] before the run ends).
	var snapDone atomic.Bool

	var onRunComplete func()
	if rec != nil {
		onRunComplete = func() {
			if !snapDone.CompareAndSwap(false, true) {
				return // already handled
			}
			finalizeSnap(rec, eng, cfg, *snapTag, resolvedSnapDir)
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
	// double-finalise when the run completed AND the user then pressed [q].
	if rec != nil && snapDone.CompareAndSwap(false, true) {
		finalizeSnap(rec, eng, cfg, *snapTag, resolvedSnapDir)
	}
}

// finalizeSnap drains the recorder, aggregates stats, and writes the .snap
// file to dir. Errors are printed to stderr; the process continues.
func finalizeSnap(rec *snap.DefaultRecorder, eng *engine.Engine, cfg *config.Config, tag, dir string) {
	fmt.Println("Finalizing snapshot...")

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
		_, _ = fmt.Fprintf(os.Stderr, "Error finalizing snapshot: %v\n", err)
		return
	}

	filename := snap.FileName(tag, endTime)
	outPath := filepath.Join(dir, filename)

	if err := snap.Write(snapData, outPath); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error writing snapshot: %v\n", err)
		return
	}

	fmt.Printf("✓ Snapshot saved → %s\n", outPath)

	if dropped := rec.Dropped(); dropped > 0 {
		fmt.Printf("⚠  %d entries were dropped (channel full) — consider a lower --snap-sample rate\n", dropped)
	}
}
