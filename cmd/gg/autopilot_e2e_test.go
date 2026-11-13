package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// kneeServer answers fast until the arrival rate passes knee requests per
// second, then slowly: a service with a capacity cliff.
type kneeServer struct {
	knee int
	mu   sync.Mutex
	seen []time.Time
}

func (k *kneeServer) over() bool {
	now := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	k.seen = append(k.seen, now)
	cut := 0
	for cut < len(k.seen) && now.Sub(k.seen[cut]) > time.Second {
		cut++
	}
	k.seen = k.seen[cut:]
	return len(k.seen) > k.knee
}

func newKneeServer(t *testing.T, knee int) *httptest.Server {
	t.Helper()
	k := &kneeServer{knee: knee}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if k.over() {
			time.Sleep(350 * time.Millisecond)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ggRun is a finished headless gg run and everything it printed.
type ggRun struct {
	events   []map[string]any
	snapPath string
}

func (r ggRun) named(name string) []map[string]any {
	var out []map[string]any
	for _, e := range r.events {
		if e["event"] == name {
			out = append(out, e)
		}
	}
	return out
}

func (r ggRun) apStates() []string {
	var out []string
	for _, e := range r.named("autopilot") {
		out = append(out, e["state"].(string))
	}
	return out
}

func (r ggRun) autopilotMarks() []string {
	var out []string
	for _, e := range r.named("mark") {
		if e["source"] == "autopilot" {
			out = append(out, e["label"].(string))
		}
	}
	return out
}

// runGG runs the test binary as gg --headless and collects its JSON events.
func runGG(t *testing.T, timeout time.Duration, args ...string) ggRun {
	t.Helper()
	snapDir := t.TempDir()
	full := append([]string{"--headless", "--reporter", "json", "--heartbeat-interval", "1s", "--control", "none",
		"--snap", "--snap-dir", snapDir, "--snap-tag", "e2e"}, args...)
	run := runGGNoSnap(t, full...)
	if matches, _ := filepath.Glob(filepath.Join(snapDir, "*.snap")); len(matches) == 1 {
		run.snapPath = matches[0]
	}
	return run
}

// runGGNoSnap runs the test binary as gg with exactly args and collects its JSON events.
func runGGNoSnap(t *testing.T, args ...string) ggRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Env = append(os.Environ(), "GG_TEST_RUN_AS_GG=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var run ggRun
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var ev map[string]any
		if json.Unmarshal(sc.Bytes(), &ev) == nil {
			run.events = append(run.events, ev)
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("gg exited with %v after %d events\nstderr:\n%s", err, len(run.events), stderr.String())
	}
	return run
}

func httpFile(t *testing.T, url string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "req.http")
	if err := os.WriteFile(path, []byte("GET "+url+"/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAutopilotE2E_FindCeilingDiscoversTheKnee(t *testing.T) {
	if testing.Short() {
		t.Skip("runs for about a minute")
	}
	t.Parallel()
	srv := newKneeServer(t, 100)
	run := runGG(t, 3*time.Minute, "--http-file", httpFile(t, srv.URL),
		"--autopilot", "find-ceiling", "--start-rps", "30", "--step-pct", "20", "--max-p99", "200ms",
		"--step-interval", "3s", "--confirm-hold", "6s", "--min-samples", "30", "--max-rps", "300", "--duration", "3m")

	states := run.apStates()
	for _, want := range []string{"stepping", "breached", "confirming", "done"} {
		if !contains(states, want) {
			t.Errorf("states %v lack %q", states, want)
		}
	}
	done := run.named("autopilot")[len(states)-1]
	ceiling, _ := done["ceiling_rps"].(float64)
	if done["outcome"] != "ceiling_found" || ceiling < 80 || ceiling > 95 {
		t.Fatalf("final autopilot event = %v, want ceiling_found between 80 and 95 RPS", done)
	}

	marks := run.autopilotMarks()
	if len(marks) < 2 || !strings.Contains(marks[0], "ceiling breached at") || !strings.Contains(marks[0], "backing off") {
		t.Errorf("autopilot marks = %q", marks)
	}
	if fin := run.named("finished"); len(fin) != 1 || fin[0]["outcome"] != "ceiling_found" || fin[0]["ceiling_rps"] != ceiling {
		t.Errorf("finished event = %v", fin)
	}
	if run.named("stopped") != nil {
		t.Error("the controller ends the run itself, which is a finish, not a stop")
	}

	s, err := snap.Read(run.snapPath)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	ap := s.Meta.Autopilot
	if ap == nil || ap.Mode != "find-ceiling" || ap.Outcome != "ceiling_found" || float64(ap.CeilingRPS) != ceiling || len(ap.Events) < 5 {
		t.Fatalf("snapshot autopilot = %+v", ap)
	}
	var tagged bool
	for _, m := range s.Meta.Marks {
		tagged = tagged || (m.Source == "autopilot" && strings.Contains(m.Label, "ceiling breached"))
	}
	if !tagged {
		t.Errorf("snapshot marks = %+v, want the autopilot's breach mark", s.Meta.Marks)
	}
}

func TestAutopilotE2E_FindCeilingNotReachedWhenTheTargetNeverDegrades(t *testing.T) {
	if testing.Short() {
		t.Skip("runs for about 20 seconds")
	}
	t.Parallel()
	srv := newKneeServer(t, 1_000_000)
	run := runGG(t, 2*time.Minute, "--http-file", httpFile(t, srv.URL),
		"--autopilot", "find-ceiling", "--start-rps", "20", "--step-pct", "50", "--max-rps", "60", "--max-p99", "200ms",
		"--step-interval", "3s", "--min-samples", "30", "--duration", "2m")

	done := run.named("autopilot")[len(run.apStates())-1]
	if done["outcome"] != "ceiling_not_reached" || done["ceiling_rps"] != 60.0 {
		t.Fatalf("final autopilot event = %v, want ceiling_not_reached at the 60 RPS cap", done)
	}
	if contains(run.apStates(), "breached") {
		t.Errorf("a healthy target must never breach: %v", run.apStates())
	}
	s, err := snap.Read(run.snapPath)
	if err != nil || s.Meta.Autopilot == nil || s.Meta.Autopilot.Outcome != "ceiling_not_reached" {
		t.Fatalf("snapshot autopilot = %+v (%v)", s.Meta.Autopilot, err)
	}
}

func TestAutopilotE2E_SLOGuardClampsOnASpikeAndDoesNotFlap(t *testing.T) {
	if testing.Short() {
		t.Skip("runs for about 40 seconds")
	}
	t.Parallel()
	srv := newKneeServer(t, 100)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "gg.yaml")
	if err := os.WriteFile(filepath.Join(dir, "req.http"), []byte("GET "+srv.URL+"/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := fmt.Sprintf("config:\n  httpFile: req.http\nstages:\n  - {duration: 6s, target_rps: 30}\n  - {duration: 0s, target_rps: 160}\n" +
		"  - {duration: 16s, target_rps: 160}\n  - {duration: 0s, target_rps: 30}\n  - {duration: 12s, target_rps: 30}\n")
	if err := os.WriteFile(cfg, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	run := runGG(t, 3*time.Minute, "--config", cfg, "--autopilot", "slo-guard", "--max-p99", "200ms",
		"--step-interval", "2s", "--recover-hold", "3s", "--min-samples", "30")

	for _, e := range run.named("autopilot") {
		t.Logf("t=%v %v hold=%v %v", e["elapsed_s"], e["state"], e["hold_rps"], e["reason"])
	}
	var clamps []map[string]any
	for _, e := range run.named("autopilot") {
		if e["state"] == "clamped" && strings.HasPrefix(e["reason"].(string), "clamped to") {
			clamps = append(clamps, e)
			if rps, _ := e["hold_rps"].(float64); rps >= 160 || rps < 1 {
				t.Errorf("a clamp must pull the rate under the spike: %v", e)
			}
		}
	}
	if len(clamps) == 0 {
		t.Fatalf("the spike (160 RPS against a 100 RPS knee) never tripped the guard: %v", run.apStates())
	}
	if len(clamps) > 4 {
		t.Errorf("the guard clamped %d times; it must settle, not oscillate", len(clamps))
	}
	if marks := run.autopilotMarks(); len(marks) == 0 || !strings.Contains(marks[0], "SLO Guard triggered") {
		t.Errorf("autopilot marks = %q", marks)
	}
	if !contains(run.apStates(), "releasing") && len(clamps) < 3 {
		t.Errorf("the guard neither released in steps nor reached its flap limit: %v", run.apStates())
	}
	last := run.named("autopilot")[len(run.apStates())-1]
	if last["state"] != "done" || last["outcome"] != "completed" {
		t.Errorf("final autopilot event = %v, want completed", last)
	}
	// After the spike the plan falls under the clamp, so the guard lets go.
	var released bool
	for _, e := range run.named("autopilot") {
		released = released || strings.HasPrefix(e["reason"].(string), "hold released")
	}
	if !released {
		t.Errorf("the guard never handed the rate back: %v", run.apStates())
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
