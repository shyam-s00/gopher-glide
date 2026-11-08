package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// tool calls an MCP tool and returns its structured result.
func (c *mcpChild) tool(id int, name string, args map[string]any) (out map[string]any, isError bool, text string) {
	c.t.Helper()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	res := c.call(id, "tools/call", string(params))
	if res["error"] != nil {
		c.t.Fatalf("%s: protocol error %v", name, res["error"])
	}
	result := res["result"].(map[string]any)
	isError, _ = result["isError"].(bool)
	if content, ok := result["content"].([]any); ok && len(content) > 0 {
		text, _ = content[0].(map[string]any)["text"].(string)
	}
	out, _ = result["structuredContent"].(map[string]any)
	return out, isError, text
}

func targetServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/boom" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// A real simulation, driven end to end through the MCP server.
func TestMCPCommand_RealSimulation_StartTelemetryStopWithSnapshot(t *testing.T) {
	srv, hits := targetServer(t)
	snapDir := t.TempDir()
	c := startMCPChildEnv(t, []string{"GG_SNAP_DIR=" + snapDir})
	c.handshake()

	out, isErr, text := c.tool(10, "start_simulation", map[string]any{
		"http_content": "GET " + srv.URL + "/ok\n\n###\nGET " + srv.URL + "/boom\n",
		"profile":      "load", "peak_rps": 20, "duration": "20s", "snap": true, "snap_tag": "smoke",
	})
	if isErr {
		t.Fatalf("start_simulation: %s", text)
	}
	id, _ := out["session_id"].(string)
	if !strings.HasPrefix(id, "sim_") || out["snapshot"] != true {
		t.Fatalf("start output = %v", out)
	}

	var tel map[string]any
	deadline := time.Now().Add(15 * time.Second)
	for n := 11; time.Now().Before(deadline); n++ {
		tel, isErr, text = c.tool(n, "get_live_telemetry", map[string]any{"session_id": id})
		if isErr {
			t.Fatalf("telemetry: %s", text)
		}
		if m, ok := tel["metrics"].(map[string]any); ok && m["total_requests"].(float64) > 5 && tel["error_mix"] != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	m, _ := tel["metrics"].(map[string]any)
	if tel["state"] != "running" || m == nil || m["total_requests"].(float64) <= 5 {
		t.Fatalf("telemetry never showed live numbers: %v", tel)
	}
	mix, _ := tel["error_mix"].(map[string]any)
	if mix == nil || mix["http_5xx"].(float64) == 0 {
		t.Errorf("the /boom endpoint returns 503, so error_mix must show 5xx: %v", tel["error_mix"])
	}
	if tel["elapsed_s"].(float64) <= 0 || hits.Load() == 0 {
		t.Errorf("elapsed=%v hits=%d", tel["elapsed_s"], hits.Load())
	}
	if size, _ := json.Marshal(tel); len(size) > 1500 {
		t.Errorf("telemetry is %d bytes, want <= 1500: %s", len(size), size)
	}

	final, isErr, text := c.tool(99, "stop_simulation", map[string]any{"session_id": id})
	if isErr {
		t.Fatalf("stop_simulation: %s", text)
	}
	f, _ := final["final"].(map[string]any)
	if final["state"] != "stopped" || f == nil || f["total_requests"].(float64) <= 5 {
		t.Fatalf("final = %v", final)
	}
	path, _ := final["snapshot_path"].(string)
	if path == "" || !strings.HasPrefix(path, snapDir) || !strings.HasSuffix(path, ".snap") {
		t.Fatalf("snapshot_path = %q (want a .snap under %s)", path, snapDir)
	}
	if st, err := os.Stat(path); err != nil || st.Size() == 0 {
		t.Fatalf("snapshot file missing or empty: %v", err)
	}
	if !strings.Contains(filepath.Base(path), "smoke") {
		t.Errorf("snapshot name should carry the tag: %s", path)
	}

	// The new snapshot is readable through the snapshot tools, by session, path and tag.
	for n, args := range []map[string]any{{"session_id": id}, {"snapshot": path}, {"snapshot": "smoke"}} {
		capt, isErr, text := c.tool(100+n, "snap_capture", args)
		if isErr {
			t.Fatalf("snap_capture %v: %s", args, text)
		}
		eps, _ := capt["endpoints"].([]any)
		hdr, _ := capt["snapshot"].(map[string]any)
		if len(eps) != 2 || hdr["tag"] != "smoke" || hdr["total_requests"].(float64) < 5 {
			t.Errorf("snap_capture %v = %v", args, capt)
		}
	}
	diff, isErr, text := c.tool(110, "snap_diff", map[string]any{"baseline": path, "current": "smoke"})
	if isErr || diff["overall"] != "PASS" {
		t.Errorf("a snapshot compared with itself must pass: %v %s", diff, text)
	}

	_ = c.stdin.Close()
	if err := c.waitExit(15 * time.Second); err != nil {
		t.Fatalf("exit: %v\n%s", err, c.stderr)
	}
}

func TestMCPCommand_RealSimulation_RefusesOverCapAndBadFile(t *testing.T) {
	srv, hits := targetServer(t)
	c := startMCPChildEnv(t, []string{"GG_SNAP_DIR=" + t.TempDir()}, "--max-rps", "10", "--max-sessions", "1")
	c.handshake()

	_, isErr, text := c.tool(10, "start_simulation", map[string]any{"http_content": "GET " + srv.URL + "/ok\n", "profile": "load", "peak_rps": 500})
	if !isErr || !strings.Contains(text, "--max-rps") {
		t.Errorf("isError=%v text=%q", isErr, text)
	}
	_, isErr, text = c.tool(11, "start_simulation", map[string]any{"http_content": "GET /nope\n", "profile": "load", "peak_rps": 5})
	if !isErr || !strings.Contains(text, "line 1") {
		t.Errorf("isError=%v text=%q", isErr, text)
	}
	if hits.Load() != 0 {
		t.Errorf("refused requests sent %d requests", hits.Load())
	}

	out, isErr, text := c.tool(12, "start_simulation", map[string]any{"http_content": "GET " + srv.URL + "/ok\n", "profile": "load", "peak_rps": 5, "duration": "20s"})
	if isErr {
		t.Fatalf("start: %s", text)
	}
	_, isErr, text = c.tool(13, "start_simulation", map[string]any{"http_content": "GET " + srv.URL + "/ok\n", "profile": "load", "peak_rps": 5})
	if !isErr || !strings.Contains(text, "--max-sessions") || !strings.Contains(text, out["session_id"].(string)) {
		t.Errorf("second session: isError=%v text=%q", isErr, text)
	}
	c.tool(14, "stop_simulation", map[string]any{})
}

// Edge 23: if the MCP server is killed outright, its simulation stops by itself
// (the closed pipe ends the child's control input) and keeps its snapshot.
func TestMCPCommand_KillingServerStopsItsSimulation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses pgrep")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	srv, _ := targetServer(t)
	snapDir := t.TempDir()
	c := startMCPChildEnv(t, []string{"GG_SNAP_DIR=" + snapDir})
	c.handshake()
	_, isErr, text := c.tool(10, "start_simulation", map[string]any{
		"http_content": "GET " + srv.URL + "/ok\n", "profile": "load", "peak_rps": 10, "duration": "60s", "snap": true,
	})
	if isErr {
		t.Fatalf("start: %s", text)
	}

	pid := childPID(t, c.cmd.Process.Pid)
	if err := c.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = c.cmd.Wait()

	deadline := time.Now().Add(20 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("simulation process %d outlived its killed parent", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
	snaps, _ := filepath.Glob(filepath.Join(snapDir, "*.snap"))
	if len(snaps) != 1 {
		t.Errorf("the orphan should have finalized its snapshot, found %v", snaps)
	}
}

func childPID(t *testing.T, parent int) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("pgrep", "-P", strconv.Itoa(parent)).Output()
		if f := strings.Fields(string(out)); len(f) > 0 {
			pid, _ := strconv.Atoi(f[0])
			return pid
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no child process found")
	return 0
}

func processAlive(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	if err != nil {
		return false
	}
	state := strings.TrimSpace(string(out))
	return state != "" && !strings.HasPrefix(state, "Z")
}

// The headless flag on its own: closing stdin stops the run gracefully.
func TestHeadless_ControlExitOnEOF_RealRun(t *testing.T) {
	srv, _ := targetServer(t)
	dir := t.TempDir()
	httpFile := filepath.Join(dir, "r.http")
	if err := os.WriteFile(httpFile, []byte("GET "+srv.URL+"/ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapDir := filepath.Join(dir, "snaps")
	cmd := exec.Command(os.Args[0], "--headless", "--reporter", "json", "--heartbeat-interval", "1s",
		"--control-exit-on-eof", "--http-file", httpFile, "--profile", "load", "--peak-rps", "10", "--duration", "60s", "--snap")
	cmd.Env = append(os.Environ(), "GG_TEST_RUN_AS_GG=1", "GG_SNAP_DIR="+snapDir)
	in, _ := cmd.StdinPipe()
	outPipe, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	events := make(chan map[string]any, 64)
	go func() {
		sc := bufio.NewScanner(outPipe)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				events <- m
			}
		}
		close(events)
	}()

	if ev := <-events; ev["event"] != "snap" && ev["event"] != "started" {
		t.Fatalf("first event = %v", ev)
	}
	time.Sleep(1500 * time.Millisecond) // let it run a little
	_ = in.Close()

	var stopped map[string]any
	deadline := time.After(20 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				break loop
			}
			if ev["event"] == "stopped" {
				stopped = ev
			}
		case <-deadline:
			t.Fatalf("no exit after stdin closed; stderr:\n%s", stderr.String())
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("exit: %v\n%s", err, stderr.String())
	}
	msg, _ := stopped["message"].(string)
	if stopped == nil || !strings.Contains(msg, "Snapshot saved") || stopped["total_requests"].(float64) == 0 {
		t.Fatalf("stopped = %v", stopped)
	}
	if snaps, _ := filepath.Glob(filepath.Join(snapDir, "*.snap")); len(snaps) != 1 {
		t.Errorf("snapshots = %v", snaps)
	}
	_, _ = fmt.Fprint(io.Discard, "")
}

// Steering a real run: the rate follows a bias, a mark carries the offset the
// agent supplied, and both end up in the snapshot.
func TestMCPCommand_RealSimulation_SteerRateAndMark(t *testing.T) {
	srv, _ := targetServer(t)
	snapDir := t.TempDir()
	c := startMCPChildEnv(t, []string{"GG_SNAP_DIR=" + snapDir}, "--max-rps", "200")
	c.handshake()

	out, isErr, text := c.tool(10, "start_simulation", map[string]any{
		"http_content": "GET " + srv.URL + "/ok\n", "profile": "load", "peak_rps": 20, "duration": "40s", "snap": true,
	})
	if isErr {
		t.Fatalf("start: %s", text)
	}
	id := out["session_id"].(string)

	id2 := 20
	telemetry := func(ok func(map[string]any) bool) map[string]any {
		t.Helper()
		var tel map[string]any
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
			id2++
			tel, _, _ = c.tool(id2, "get_live_telemetry", map[string]any{"session_id": id})
			if m, _ := tel["metrics"].(map[string]any); m != nil && ok(m) {
				return tel
			}
		}
		t.Fatalf("telemetry never matched: %v", tel)
		return nil
	}
	tel := telemetry(func(m map[string]any) bool { return m["total_requests"].(float64) > 0 })
	seen := tel["elapsed_s"].(float64)
	before := tel["metrics"].(map[string]any)["target_rps"].(float64)

	adj, isErr, text := c.tool(11, "steer_traffic", map[string]any{"action": "adjust_rps", "delta": 50})
	if isErr || adj["applied"] != true || adj["bias_total"].(float64) != 50 || adj["target_rps_before"].(float64) <= 0 {
		t.Fatalf("adjust: %v %s", adj, text)
	}
	tel = telemetry(func(m map[string]any) bool { return m["target_rps"].(float64) >= before+49 })
	if steers, _ := tel["recent_steers"].([]any); len(steers) != 1 {
		t.Errorf("recent_steers = %v", tel["recent_steers"])
	}

	mark, isErr, text := c.tool(12, "steer_traffic", map[string]any{"action": "mark", "label": "saw it", "at_elapsed_s": seen})
	if isErr || mark["elapsed_s"].(float64) != seen || mark["clamped"] == true {
		t.Fatalf("mark: %v %s (offset sent %v)", mark, text, seen)
	}

	// Let the rate settle, then aim for an absolute rate.
	time.Sleep(2 * time.Second)
	var set map[string]any
	for n := 30; n < 40; n++ {
		set, isErr, text = c.tool(n, "steer_traffic", map[string]any{"action": "set_rps", "target_rps": 30})
		if !isErr || !strings.Contains(text, "still taking effect") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if isErr || set["bias_total"] == nil {
		t.Fatalf("set_rps: %v %s", set, text)
	}

	_, isErr, text = c.tool(50, "steer_traffic", map[string]any{"action": "set_rps", "target_rps": 500})
	if !isErr || !strings.Contains(text, "--max-rps") {
		t.Errorf("above-cap set_rps: isError=%v %q", isErr, text)
	}

	final, isErr, text := c.tool(60, "stop_simulation", map[string]any{"session_id": id})
	if isErr {
		t.Fatalf("stop: %s", text)
	}
	if _, isErr, text = c.tool(61, "steer_traffic", map[string]any{"action": "mark", "label": "late", "session_id": id}); !isErr || !strings.Contains(text, "session_not_running") {
		t.Errorf("mark after stop: isError=%v %q", isErr, text)
	}

	path, _ := final["snapshot_path"].(string)
	sn, err := snap.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range sn.Meta.Marks {
		found = found || (m.Label == "saw it" && m.ElapsedS == seen)
	}
	if !found || len(sn.Meta.BiasEvents) == 0 {
		t.Errorf("snapshot should carry the mark at the supplied offset %v and the bias changes: marks=%v bias=%v", seen, sn.Meta.Marks, sn.Meta.BiasEvents)
	}
}
