package main

import (
	"bufio"
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
	"github.com/shyam-s00/gopher-glide/internal/ui"
	"github.com/shyam-s00/gopher-glide/internal/version"
)

func main() {
	fmt.Printf("gg (Gopher Glide) %s (commit:%s) built %s\n",
		version.Version, version.GitCommit, version.GetBuildDate())

	if len(os.Args) < 2 {
		fmt.Println("Usage: gg <config-file> [--snap] [--snap-tag TAG] [--snap-dir DIR] [--snap-sample RATE]")
		fmt.Println("       gg snap <list|view|diff> [--snap-dir DIR]")
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
	// 0 is the sentinel for "not explicitly set on CLI"; effective values are
	// resolved after config.yaml is loaded (CLI > config.yaml > hard default).
	fs := flag.NewFlagSet("gg", flag.ExitOnError)
	snapEnabled := fs.Bool("snap", false, "capture a behavioral snapshot after the run")
	snapTag := fs.String("snap-tag", "", "tag to attach to the snapshot (e.g. v1.2.0-pre)")
	snapDir := fs.String("snap-dir", "", "override the default snapshot directory")
	snapSample := fs.Float64("snap-sample", 0, "fraction of responses to body-sample for schema inference (0 = use config/default of 5%)")
	snapMaxSamples := fs.Int("snap-max-samples", 0, "max body samples retained per endpoint via reservoir sampling (0 = use config/default of 200)")
	snapMaxBodyKB := fs.Int("snap-max-body-kb", 0, "per-endpoint byte budget for stored body samples in KB (0 = no byte-based limit)")
	headless := fs.Bool("headless", false, "run without interactive TUI — emits structured heartbeat logs (for CI)")
	reporter := fs.String("reporter", "text", "output format in headless mode: text | json")
	_ = fs.Parse(os.Args[2:])

	// Track which flags were explicitly provided so we can apply the correct
	// precedence: CLI (explicit) > config.yaml > hard default.
	var cliSnapSampleSet, cliMaxSamplesSet, cliMaxBodyKBSet bool

	// Any snap-specific flag passed explicitly implicitly enables snapping,
	// so users don't need to type both --snap and e.g. --snap-dir.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "snap-dir", "snap-tag", "snap-sample", "snap-max-samples", "snap-max-body-kb":
			*snapEnabled = true
		}
		switch f.Name {
		case "snap-sample":
			cliSnapSampleSet = true
		case "snap-max-samples":
			cliMaxSamplesSet = true
		case "snap-max-body-kb":
			cliMaxBodyKBSet = true
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

	// ── resolve effective snap tuning values ──────────────────────────────────
	// Precedence: explicit CLI flag > config.yaml snap: block > hard default.
	// 0 is the sentinel for "not set at this level".
	effectiveSampleRate := 0.05 // hard default: 5 %
	if cliSnapSampleSet {
		effectiveSampleRate = *snapSample
	} else if cfg.Snap.SampleRate > 0 {
		effectiveSampleRate = cfg.Snap.SampleRate
	}

	effectiveMaxSamples := 0 // 0 → recorder uses DefaultMaxBodySamples (200)
	if cliMaxSamplesSet {
		effectiveMaxSamples = *snapMaxSamples
	} else if cfg.Snap.MaxSamples > 0 {
		effectiveMaxSamples = cfg.Snap.MaxSamples
	}

	effectiveMaxBodyKB := 0 // 0 → no byte-based limit
	if cliMaxBodyKBSet {
		effectiveMaxBodyKB = *snapMaxBodyKB
	} else if cfg.Snap.MaxBodyKB > 0 {
		effectiveMaxBodyKB = cfg.Snap.MaxBodyKB
	}

	// ── validate snap tuning values ───────────────────────────────────────────
	// Done before any I/O so bad input is caught as early as possible.
	// Covers both explicit CLI flags and values sourced from config.yaml.
	if *snapEnabled {
		if err := validateSnapTuning(effectiveSampleRate, effectiveMaxSamples, effectiveMaxBodyKB); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "snap flag error: %v\n", err)
			os.Exit(1)
		}
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
		recOpts := []snap.RecorderOption{}
		if effectiveMaxSamples > 0 {
			recOpts = append(recOpts, snap.WithMaxBodySamples(effectiveMaxSamples))
		}
		if effectiveMaxBodyKB > 0 {
			recOpts = append(recOpts, snap.WithMaxBodyBytes(int64(effectiveMaxBodyKB)*1024))
		}
		rec = snap.NewDefaultRecorder(0, recOpts...)
		fmt.Printf("📸 Snapping → %s  (sample=%.0f%%, max-samples=%d, max-body-kb=%d)\n",
			resolvedSnapDir,
			effectiveSampleRate*100,
			resolveDisplayMaxSamples(effectiveMaxSamples),
			effectiveMaxBodyKB,
		)
	}

	// ── build engine ──────────────────────────────────────────────────────────
	var engineOpts []engine.EngineOption
	if rec != nil {
		engineOpts = append(engineOpts,
			engine.WithRecorder(rec),
			engine.WithSampleRate(effectiveSampleRate),
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
			status, err := finalizeSnapResult(rec, eng, cfg, *snapTag, resolvedSnapDir,
				effectiveSampleRate, effectiveMaxSamples, effectiveMaxBodyKB)
			if err != nil {
				return fmt.Sprintf("⚠  snap error: %v", err)
			}
			return status
		}
	}

	fmt.Println("Starting...")
	renderer := ui.New(*headless)
	if *headless {
		if hr, ok := renderer.(*ui.HeadlessRenderer); ok {
			hr.Reporter = *reporter
		}
	}
	if err := renderer.Run(eng, cfg, specs, ui.RunOptions{
		Snapping:      *snapEnabled,
		SnapDir:       resolvedSnapDir,
		OnRunComplete: onRunComplete,
	}); err != nil {
		fmt.Printf("Error running: %v\n", err)
		os.Exit(1)
	}

	// ── early-quit fallback ───────────────────────────────────────────────────
	// Reached when the user presses [q] before all stages completed, meaning
	// onRunComplete was never called by the TUI.  The CAS ensures we don't
	// double-finalise when the run completed AND the user then pressed [q].
	// Printing is safe here: tui.Start has returned and the terminal is restored.
	if rec != nil && snapDone.CompareAndSwap(false, true) {
		fmt.Println("Finalizing snapshot...")
		status, finalErr := finalizeSnapResult(rec, eng, cfg, *snapTag, resolvedSnapDir,
			effectiveSampleRate, effectiveMaxSamples, effectiveMaxBodyKB)
		if finalErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Error: %v\n", finalErr)
		} else {
			fmt.Println(status)
		}
	}
}

// finalizeSnapResult drains the recorder, aggregates stats, and writes the
// .snap file to dir. Returns a human-readable status line on success.
// It does not write to stdout or stderr, making it safe to call while the
// TUI alt-screen is active.
func finalizeSnapResult(rec *snap.DefaultRecorder, eng *engine.Engine, cfg *config.Config,
	tag, dir string, sampleRate float64, maxSamples, maxBodyKB int) (string, error) {
	endTime := eng.GetEndTime()
	if endTime.IsZero() {
		endTime = time.Now()
	}

	snapData, err := rec.Finalize(snap.RunMeta{
		Tag:        tag,
		Config:     cfg,
		StartTime:  eng.GetStartTime(),
		EndTime:    endTime,
		PeakRPS:    cfg.PeakRPS(),
		SampleRate: sampleRate,
		MaxSamples: maxSamples,
		MaxBodyKB:  maxBodyKB,
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
	case "diff":
		runSnapDiff(args[1:])
	case "assert":
		runSnapAssert(args[1:])
	case "prune":
		runSnapPrune(args[1:])
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown snap subcommand %q\n\n", args[0])
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
		_, _ = fmt.Fprintf(os.Stderr, "snap list: resolve directory: %v\n", err)
		os.Exit(1)
	}

	summaries, err := snap.ListAll(dir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap list: %v\n", err)
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

	info, err := resolveSnapTarget(target, dir)
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

func runSnapDiff(args []string) {
	fs := flag.NewFlagSet("gg snap diff", flag.ExitOnError)
	snapDir := fs.String("snap-dir", "", "override the default snapshot directory")

	// Extract the two positional args before any flags, following the same
	// pattern as runSnapView.  Accepts: IDs, tag names, or file paths.
	var positional []string
	flagStart := len(args)
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			flagStart = i
			break
		}
		positional = append(positional, a)
	}
	_ = fs.Parse(args[flagStart:])
	// If both positionals weren't found before the first flag, pick up
	// whatever remains after flag parsing (handles: --snap-dir /x id1 id2).
	for len(positional) < 2 {
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest...)
		break
	}

	if len(positional) != 2 {
		_, _ = fmt.Fprintln(os.Stderr, "Usage: gg snap diff <id1|tag1|file1> <id2|tag2|file2> [--snap-dir DIR]")
		os.Exit(1)
	}

	dir, err := snap.ResolveSnapDir(*snapDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap diff: resolve directory: %v\n", err)
		os.Exit(1)
	}

	baseInfo, err := resolveSnapTarget(positional[0], dir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap diff: baseline %q: %v\n", positional[0], err)
		os.Exit(1)
	}
	currInfo, err := resolveSnapTarget(positional[1], dir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap diff: current %q: %v\n", positional[1], err)
		os.Exit(1)
	}

	baseSnap, err := snap.Read(baseInfo.Path)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap diff: read baseline %q: %v\n", baseInfo.Path, err)
		os.Exit(1)
	}
	currSnap, err := snap.Read(currInfo.Path)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap diff: read current %q: %v\n", currInfo.Path, err)
		os.Exit(1)
	}

	result := snap.Diff(baseSnap, currSnap, snap.DefaultDiffOptions())

	if err := tui.StartSnapDiff(result, *baseInfo, *currInfo); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap diff: %v\n", err)
		os.Exit(1)
	}
}

// resolveSnapTarget resolves a snapshot target string (numeric ID, tag name, or
// file path) to a SnapInfo. This shared helper is used by both runSnapView and
// runSnapDiff to avoid duplicating lookup logic.
func resolveSnapTarget(target, dir string) (*snap.SnapInfo, error) {
	if id, err := strconv.Atoi(target); err == nil {
		return snap.FindByID(dir, id)
	}
	if _, err := os.Stat(target); err == nil {
		si := snap.SnapInfo{Path: target, FileName: filepath.Base(target)}
		return &si, nil
	}
	return snap.FindByTag(dir, target)
}

func runSnapAssert(args []string) {
	fs := flag.NewFlagSet("gg snap assert", flag.ExitOnError)
	snapDir := fs.String("snap-dir", "", "override the default snapshot directory")
	baseline := fs.String("baseline", "", "baseline snapshot: numeric ID, tag name, or file path (required)")
	current := fs.String("current", "", "current snapshot: numeric ID, tag name, or file path (required)")
	latencyReg := fs.Float64("latency-regression", 20, "P99 latency % increase that triggers REGRESSION")
	errorDelta := fs.Float64("error-rate-delta", 0.05, "error rate absolute increase (0–1) that triggers REGRESSION")
	payloadPct := fs.Float64("payload-size-delta", 50, "avg payload size % increase that triggers WARN")
	denyRemoved := fs.Bool("deny-removed-fields", false, "treat removed schema fields as REGRESSION (default: WARN)")
	failOnWarn := fs.Bool("fail-on-warn", false, "exit non-zero on WARN verdicts in addition to REGRESSION")
	reporter := fs.String("reporter", "text", "output format: text | json | md")
	out := fs.String("out", "", "write report to this file path instead of stdout")
	_ = fs.Parse(args)

	if *baseline == "" || *current == "" {
		_, _ = fmt.Fprintln(os.Stderr, "Usage: gg snap assert --baseline <id|tag|file> --current <id|tag|file> [flags]")
		_, _ = fmt.Fprintln(os.Stderr, "")
		fs.PrintDefaults()
		os.Exit(1)
	}

	if err := validateAssertFlags(*latencyReg, *errorDelta, *payloadPct); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap assert: invalid flag: %v\n", err)
		os.Exit(1)
	}

	dir, err := snap.ResolveSnapDir(*snapDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap assert: resolve directory: %v\n", err)
		os.Exit(1)
	}

	baseInfo, err := resolveSnapTarget(*baseline, dir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap assert: baseline %q: %v\n", *baseline, err)
		os.Exit(1)
	}
	currInfo, err := resolveSnapTarget(*current, dir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap assert: current %q: %v\n", *current, err)
		os.Exit(1)
	}

	baseSnap, err := snap.Read(baseInfo.Path)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap assert: read baseline %q: %v\n", baseInfo.Path, err)
		os.Exit(1)
	}
	currSnap, err := snap.Read(currInfo.Path)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap assert: read current %q: %v\n", currInfo.Path, err)
		os.Exit(1)
	}

	opts := snap.AssertOptions{
		DiffOptions: snap.DiffOptions{
			LatencyP99RegressionPct:    *latencyReg,
			ErrorRateDeltaThreshold:    *errorDelta,
			PayloadSizeAvgPctThreshold: *payloadPct,
			DenyRemovedFields:          *denyRemoved,
		},
		FailOnWarn: *failOnWarn,
	}

	result := snap.Assert(baseSnap, currSnap, opts)

	report, err := snap.FormatAssertResult(result, *reporter)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap assert: format report: %v\n", err)
		os.Exit(1)
	}

	if *out != "" {
		if err := os.WriteFile(*out, []byte(report), 0644); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "snap assert: write output file %q: %v\n", *out, err)
			os.Exit(1)
		}
		fmt.Printf("Report written to %s\n", *out)
	} else {
		fmt.Print(report)
	}

	if !result.Passed {
		os.Exit(1)
	}
}

func runSnapPrune(args []string) {
	fs := flag.NewFlagSet("gg snap prune", flag.ExitOnError)
	snapDir := fs.String("snap-dir", "", "override the default snapshot directory")
	keepLast := fs.Int("keep-last", 0, "keep the N most-recent snapshots; delete all older ones")
	olderThan := fs.String("older-than", "", "delete snapshots older than this duration (e.g. 30d, 720h)")
	tag := fs.String("tag", "", "delete all snapshots whose tag matches this value exactly")
	ids := fs.String("ids", "", "comma-separated list of snapshot IDs to delete (e.g. 1,3,5) — used by the IDE Snap Explorer tool window")
	dryRun := fs.Bool("dry-run", false, "preview candidates without deleting anything")
	yes := fs.Bool("yes", false, "skip interactive confirmation prompt (required for non-interactive / plugin-driven calls)")
	reporter := fs.String("reporter", "text", "output format: text | json  (json is the stable contract for the JetBrains plugin)")
	_ = fs.Parse(args)

	// ── validate: at least one filter required ────────────────────────────────
	if *keepLast == 0 && *olderThan == "" && *tag == "" && *ids == "" {
		_, _ = fmt.Fprintln(os.Stderr, "snap prune: at least one filter flag is required")
		_, _ = fmt.Fprintln(os.Stderr, "")
		fs.PrintDefaults()
		os.Exit(1)
	}

	// ── parse filter flags ────────────────────────────────────────────────────
	olderThanDur, err := snap.ParseOlderThan(*olderThan)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap prune: %v\n", err)
		os.Exit(1)
	}

	idList, err := snap.ParseIDs(*ids)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap prune: %v\n", err)
		os.Exit(1)
	}

	if *keepLast < 0 {
		_, _ = fmt.Fprintln(os.Stderr, "snap prune: --keep-last must be >= 0")
		os.Exit(1)
	}

	// ── resolve snap directory and list snapshots ─────────────────────────────
	dir, err := snap.ResolveSnapDir(*snapDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap prune: resolve directory: %v\n", err)
		os.Exit(1)
	}

	infos, err := snap.List(dir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap prune: list snapshots: %v\n", err)
		os.Exit(1)
	}

	// ── select candidates ─────────────────────────────────────────────────────
	candidates := snap.SelectForPrune(infos, snap.PruneOptions{
		KeepLast:  *keepLast,
		OlderThan: olderThanDur,
		Tag:       *tag,
		IDs:       idList,
	})

	// ── no-op path ────────────────────────────────────────────────────────────
	if len(candidates) == 0 {
		if strings.ToLower(*reporter) == "json" {
			report := snap.BuildPruneReport(dir, candidates, *dryRun, 0, nil)
			js, _ := report.JSON()
			fmt.Println(js)
		} else {
			fmt.Println("No snapshots match the given filters — nothing to prune.")
		}
		return
	}

	// ── dry-run: preview and exit without deleting ────────────────────────────
	if *dryRun {
		if strings.ToLower(*reporter) == "json" {
			report := snap.BuildPruneReport(dir, candidates, true, 0, nil)
			js, _ := report.JSON()
			fmt.Println(js)
		} else {
			fmt.Printf("Dry run — %d snapshot(s) would be deleted from %s:\n\n", len(candidates), dir)
			printPruneCandidates(candidates)
			fmt.Println("\nRe-run without --dry-run to apply.")
		}
		return
	}

	// ── interactive confirmation (skipped when --yes or --reporter json) ──────
	isJSON := strings.ToLower(*reporter) == "json"
	if !*yes && !isJSON {
		fmt.Printf("The following %d snapshot(s) in %s will be permanently deleted:\n\n", len(candidates), dir)
		printPruneCandidates(candidates)
		fmt.Printf("\nProceed? [y/N] ")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			// Scan() returned false: either EOF (stdin closed/piped with no input)
			// or a read error. Treat both as an explicit abort rather than silently
			// doing nothing, so the user understands why nothing was deleted.
			if err := scanner.Err(); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "snap prune: failed to read confirmation: %v\n", err)
				os.Exit(1)
			}
			// EOF — e.g. stdin redirected from /dev/null or a closed pipe.
			fmt.Println("\nAborted (no input).")
			return
		}
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return
		}
	}

	// ── delete ────────────────────────────────────────────────────────────────
	deleted, errs := snap.Delete(candidates)

	if isJSON {
		report := snap.BuildPruneReport(dir, candidates, false, deleted, errs)
		js, _ := report.JSON()
		fmt.Println(js)
		if len(errs) > 0 {
			os.Exit(1)
		}
		return
	}

	// text reporter
	fmt.Printf("✓ Deleted %d of %d snapshot(s) from %s\n", deleted, len(candidates), dir)
	if len(errs) > 0 {
		for _, e := range errs {
			_, _ = fmt.Fprintf(os.Stderr, "  ⚠ %v\n", e)
		}
		os.Exit(1)
	}
}

// printPruneCandidates writes a tabwriter-aligned table of pruning candidates
// to stdout.
func printPruneCandidates(candidates []snap.PruneCandidate) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tTAG\tDATE\tFILE\tREASON")
	_, _ = fmt.Fprintln(w, "--\t---\t----\t----\t------")
	for _, c := range candidates {
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n",
			c.ID,
			snapDisplayTag(c.Tag),
			c.Date.Format("2006-01-02 15:04"),
			c.FileName,
			c.Reason,
		)
	}
	_ = w.Flush()
}

func snapUsage() {
	_, _ = fmt.Fprintln(os.Stderr, "Usage: gg snap <subcommand> [flags]")
	_, _ = fmt.Fprintln(os.Stderr, "")
	_, _ = fmt.Fprintln(os.Stderr, "Subcommands:")
	_, _ = fmt.Fprintln(os.Stderr, "  list                                   [--snap-dir DIR]   list all saved snapshots")
	_, _ = fmt.Fprintln(os.Stderr, "  view <id|tag|file>                     [--snap-dir DIR]   view a single snapshot")
	_, _ = fmt.Fprintln(os.Stderr, "  diff <id1|tag1|file1> <id2|tag2|file2> [--snap-dir DIR]   diff two snapshots")
	_, _ = fmt.Fprintln(os.Stderr, "  assert --baseline <id|tag|file> --current <id|tag|file> [flags] [--snap-dir DIR]   CI regression gate (exits 1 on failure)")
	_, _ = fmt.Fprintln(os.Stderr, "  prune  [--keep-last N] [--older-than DURATION] [--tag TAG] [--ids 1,3,5] [--dry-run] [--yes] [--reporter text|json] [--snap-dir DIR]   delete old snapshots")
}

func snapDisplayTag(tag string) string {
	if tag == "" || tag == "run" {
		return "(untagged)"
	}
	return tag
}

// resolveDisplayMaxSamples returns the effective max-samples value for display.
// When effectiveMaxSamples is 0 the recorder will use DefaultMaxBodySamples.
func resolveDisplayMaxSamples(effectiveMaxSamples int) int {
	if effectiveMaxSamples <= 0 {
		return snap.DefaultMaxBodySamples
	}
	return effectiveMaxSamples
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

// validateSnapTuning returns an error if any effective snap tuning value falls
// outside its legal range. Called before the recorder is constructed so that
// invalid CLI or config.yaml input is caught early with a clear message.
//
//   - sampleRate must be in [0, 1]  (0 = disable body sampling entirely)
//   - maxSamples must be >= 0       (0 = use DefaultMaxBodySamples)
//   - maxBodyKB  must be >= 0       (0 = no byte-based limit)
func validateSnapTuning(sampleRate float64, maxSamples, maxBodyKB int) error {
	if sampleRate < 0 || sampleRate > 1 {
		return fmt.Errorf("--snap-sample must be in [0, 1], got %g", sampleRate)
	}
	if maxSamples < 0 {
		return fmt.Errorf("--snap-max-samples must be >= 0, got %d", maxSamples)
	}
	if maxBodyKB < 0 {
		return fmt.Errorf("--snap-max-body-kb must be >= 0, got %d", maxBodyKB)
	}
	return nil
}

// validateAssertFlags returns an error if any snap assert threshold is outside
// its legal range. Called before any snapshot I/O so bad input is caught early.
//
//   - latencyReg  must be > 0   (a % increase; 0 would always trigger, negative is nonsensical)
//   - errorDelta  must be in [0, 1]  (absolute rate change; 0 would always trigger, >1 is impossible)
//   - payloadPct  must be > 0   (a % increase; same rationale as latencyReg)
func validateAssertFlags(latencyReg, errorDelta, payloadPct float64) error {
	if latencyReg <= 0 {
		return fmt.Errorf("--latency-regression must be > 0, got %g", latencyReg)
	}
	if errorDelta <= 0 || errorDelta > 1 {
		return fmt.Errorf("--error-rate-delta must be in [0, 1], got %g", errorDelta)
	}
	if payloadPct <= 0 {
		return fmt.Errorf("--payload-size-delta must be > 0, got %g", payloadPct)
	}
	return nil
}
