package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// runSnapCLI runs `gg snap <args>` as a child and returns stdout, stderr and the exit code.
func runSnapCLI(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"snap"}, args...)...)
	cmd.Env = append(os.Environ(), "GG_TEST_RUN_AS_GG=1", "GG_SNAP_DIR="+dir)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

func twoSnaps(t *testing.T) (dir, basePath, currPath string) {
	t.Helper()
	dir = t.TempDir()
	d1 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	basePath = writeFixture(t, dir, "base", d1, fixtureEndpoint("GET:/a", 100, 0, "number", false), fixtureEndpoint("GET:/b", 50, 0, "string", false))
	currPath = writeFixture(t, dir, "head", d1.Add(24*time.Hour), fixtureEndpoint("GET:/a", 200, 0.1, "string", true), fixtureEndpoint("GET:/b", 50, 0, "string", false))
	return dir, basePath, currPath
}

func TestSnapListJSON(t *testing.T) {
	dir, base, _ := twoSnaps(t)
	out, errs, code := runSnapCLI(t, dir, "list", "--reporter", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	var rep snap.ListReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(rep.Snapshots) != 2 || rep.Snapshots[0].Tag != "base" || rep.Snapshots[0].ID != 1 || rep.Snapshots[1].ID != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Snapshots[0].Path != base || rep.Snapshots[0].Endpoints != 2 || rep.Snapshots[0].TotalRequests != 100 || rep.Snapshots[0].PeakRPS != 10 {
		t.Errorf("entry = %+v", rep.Snapshots[0])
	}
	if strings.Contains(out, "TAG") || strings.Contains(out, "----") {
		t.Errorf("json mode must not print the table: %s", out)
	}
}

func TestSnapListJSON_EmptyDirectory(t *testing.T) {
	out, errs, code := runSnapCLI(t, t.TempDir(), "list", "--reporter", "json")
	var rep snap.ListReport
	if code != 0 || json.Unmarshal([]byte(out), &rep) != nil || rep.Snapshots == nil || len(rep.Snapshots) != 0 {
		t.Errorf("exit %d out %q err %q", code, out, errs)
	}
}

func TestSnapListText_Unchanged(t *testing.T) {
	dir, _, _ := twoSnaps(t)
	out, _, code := runSnapCLI(t, dir, "list")
	if code != 0 || !strings.Contains(out, "TAG") || !strings.Contains(out, "base") {
		t.Errorf("exit %d out %q", code, out)
	}
}

func TestSnapDiffJSON_MatchesTheLibrary(t *testing.T) {
	dir, base, curr := twoSnaps(t)
	for _, args := range [][]string{
		{"diff", "base", "head", "--reporter", "json"},
		{"diff", "1", "2", "--reporter", "json"},
		{"diff", "--reporter", "json", base, curr},
	} {
		out, errs, code := runSnapCLI(t, dir, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errs)
		}
		var rep snap.DiffReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("%v: not JSON: %v\n%s", args, err, out)
		}
		b, _ := snap.Read(base)
		c, _ := snap.Read(curr)
		want := snap.Diff(b, c, snap.DefaultDiffOptions())
		if len(rep.Result.Endpoints) != len(want.Endpoints) {
			t.Fatalf("%v: endpoints = %d, want %d", args, len(rep.Result.Endpoints), len(want.Endpoints))
		}
		for i, e := range rep.Result.Endpoints {
			if e.ID != want.Endpoints[i].ID || e.Verdict != want.Endpoints[i].Verdict {
				t.Errorf("%v: endpoint %d = %s %s, want %s %s", args, i, e.ID, e.Verdict, want.Endpoints[i].ID, want.Endpoints[i].Verdict)
			}
		}
		if rep.BaselineRef.Tag != "base" || rep.CurrentRef.Tag != "head" || rep.BaselineRef.Path != base {
			t.Errorf("%v: refs = %+v %+v", args, rep.BaselineRef, rep.CurrentRef)
		}
		if rep.Result.Baseline.Tag != "base" || rep.Result.Current.Tag != "head" {
			t.Errorf("%v: result meta = %+v %+v", args, rep.Result.Baseline.Tag, rep.Result.Current.Tag)
		}
	}
}

func TestSnapViewJSON(t *testing.T) {
	dir, base, _ := twoSnaps(t)
	for _, args := range [][]string{{"view", "base", "--reporter", "json"}, {"view", "--reporter", "json", "1"}} {
		out, errs, code := runSnapCLI(t, dir, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errs)
		}
		var rep snap.ViewReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("%v: not JSON: %v\n%s", args, err, out)
		}
		if rep.Ref.Path != base || rep.Snapshot == nil || rep.Snapshot.Meta.Tag != "base" || len(rep.Snapshot.Endpoints) != 2 {
			t.Errorf("%v: report = %+v", args, rep)
		}
	}
}

func TestSnapJSON_ErrorsStayOnStderrWithNonZeroExit(t *testing.T) {
	dir, _, _ := twoSnaps(t)
	cases := [][]string{
		{"view", "nope", "--reporter", "json"},
		{"diff", "base", "nope", "--reporter", "json"},
		{"list", "--reporter", "xml"},
		{"view", "base", "--reporter", "yaml"},
	}
	for _, args := range cases {
		out, errs, code := runSnapCLI(t, dir, args...)
		if code == 0 || strings.TrimSpace(out) != "" || errs == "" {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, out, errs)
		}
	}
}
