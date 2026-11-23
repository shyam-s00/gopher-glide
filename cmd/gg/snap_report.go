package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// reportFormatFor picks a format from an explicit --format value, else from
// the output file's extension (.html or .htm means html), else Markdown.
func reportFormatFor(format, outPath string) (snap.ReportFormat, error) {
	if format != "" {
		return snap.ParseReportFormat(format)
	}
	switch strings.ToLower(filepath.Ext(outPath)) {
	case ".html", ".htm":
		return snap.ReportHTML, nil
	}
	return snap.ReportMarkdown, nil
}

// reportTitle names a run in its report: the tag, else the file name.
func reportTitle(s *snap.Snapshot, path string) string {
	if s.Meta.Tag != "" {
		return s.Meta.Tag
	}
	if path != "" {
		return strings.TrimSuffix(filepath.Base(path), snap.SnapFileExt)
	}
	return ""
}

// writeReportFile renders s and writes it to out, replacing any file there.
func writeReportFile(s *snap.Snapshot, snapPath, out string, format snap.ReportFormat) error {
	body, err := snap.RenderReport(s, reportTitle(s, snapPath), format)
	if err != nil {
		return err
	}
	return os.WriteFile(out, body, 0o644)
}

func runSnapReport(args []string) {
	fs := flag.NewFlagSet("gg snap report", flag.ExitOnError)
	snapDir := fs.String("snap-dir", "", "override the default snapshot directory")
	format := fs.String("format", "", "report format: md | html (default: from the --out extension, else md)")
	out := fs.String("out", "", "write the report to this file instead of stdout")

	var target string
	var flagArgs []string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, flagArgs = args[0], args[1:]
	} else {
		flagArgs = args
	}
	_ = fs.Parse(flagArgs)
	if target == "" && fs.NArg() > 0 {
		target = fs.Arg(0)
	}
	if target == "" {
		_, _ = fmt.Fprintln(os.Stderr, "Usage: gg snap report <id|tag|file> [--snap-dir DIR] [--format md|html] [--out FILE]")
		os.Exit(1)
	}

	f, err := reportFormatFor(*format, *out)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap report: %v\n", err)
		os.Exit(1)
	}
	dir, err := snap.ResolveSnapDir(*snapDir)
	if err == nil {
		dir, err = filepath.Abs(dir)
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap report: resolve directory: %v\n", err)
		os.Exit(1)
	}
	info, err := resolveSnapTarget(target, dir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap report: %v\n", err)
		os.Exit(1)
	}
	s, err := snap.Read(info.Path)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap report: read %q: %v\n", info.Path, err)
		os.Exit(1)
	}

	if *out == "" {
		body, err := snap.RenderReport(s, reportTitle(s, info.Path), f)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "snap report: %v\n", err)
			os.Exit(1)
		}
		_, _ = os.Stdout.Write(body)
		return
	}
	if err := writeReportFile(s, info.Path, *out, f); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "snap report: %v\n", err)
		os.Exit(1)
	}
	_, _ = fmt.Fprintf(os.Stderr, "✓ Report written → %s\n", *out)
}
