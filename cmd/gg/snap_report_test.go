package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

func TestSnapReportCLI(t *testing.T) {
	dir, _, _ := twoSnaps(t)

	t.Run("markdown to stdout by tag", func(t *testing.T) {
		out, errs, code := runSnapCLI(t, dir, "report", "head")
		if code != 0 || !strings.HasPrefix(out, "# gg run report: head") || !strings.Contains(out, "GET:/a") {
			t.Fatalf("exit %d\nstdout: %.300s\nstderr: %s", code, out, errs)
		}
	})

	t.Run("html by extension, to a file", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "r.html")
		_, errs, code := runSnapCLI(t, dir, "report", "1", "--out", out)
		body, err := os.ReadFile(out)
		if code != 0 || err != nil || !strings.Contains(string(body), "<!doctype html>") || !strings.Contains(errs, out) {
			t.Fatalf("exit %d err %v stderr %s", code, err, errs)
		}
	})

	t.Run("explicit format beats the extension", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "r.html")
		_, _, code := runSnapCLI(t, dir, "report", "1", "--format", "md", "--out", out)
		body, _ := os.ReadFile(out)
		if code != 0 || !strings.HasPrefix(string(body), "# gg run report") {
			t.Fatalf("exit %d body %.100s", code, body)
		}
	})

	for name, args := range map[string][]string{
		"no target":       {"report"},
		"unknown format":  {"report", "1", "--format", "pdf"},
		"unknown snap":    {"report", "nope"},
		"unwritable path": {"report", "1", "--out", filepath.Join(os.DevNull, "x", "r.md")},
	} {
		t.Run("fails: "+name, func(t *testing.T) {
			if out, _, code := runSnapCLI(t, dir, args...); code == 0 || out != "" {
				t.Errorf("exit %d, stdout %q; want a non-zero exit and nothing on stdout", code, out)
			}
		})
	}
}

func TestReportRunFlag_WritesTheReportWithTheSnapshot(t *testing.T) {
	srv, _ := targetServer(t)
	snapDir, reportFile := t.TempDir(), filepath.Join(t.TempDir(), "run.md")
	httpFile := filepath.Join(t.TempDir(), "req.http")
	if err := os.WriteFile(httpFile, []byte("GET "+srv.URL+"/ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := runGGNoSnap(t, "--headless", "--reporter", "json", "--control", "none", "--http-file", httpFile,
		"--profile", "smoke", "--duration", "3s", "--snap-dir", snapDir, "--report", reportFile)

	body, err := os.ReadFile(reportFile)
	if err != nil || !strings.HasPrefix(string(body), "# gg run report: ") || !strings.Contains(string(body), "/ok") {
		t.Fatalf("report file = %v, %.200s", err, body)
	}
	if matches, _ := filepath.Glob(filepath.Join(snapDir, "*.snap")); len(matches) != 1 {
		t.Errorf("--report must imply --snap: %v", matches)
	}
	fin := run.named("finished")
	if len(fin) != 1 || !strings.Contains(fin[0]["message"].(string), "Report → "+reportFile) {
		t.Errorf("finished event = %v", fin)
	}
	if _, err := snap.Read(firstSnap(t, snapDir)); err != nil {
		t.Error(err)
	}
}

func firstSnap(t *testing.T, dir string) string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*.snap"))
	if len(m) == 0 {
		t.Fatal("no snapshot written")
	}
	return m[0]
}
