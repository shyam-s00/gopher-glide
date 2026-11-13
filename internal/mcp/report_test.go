package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
	"github.com/shyam-s00/gopher-glide/internal/mcp/session/fakegg"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

func autopilotSnap(t *testing.T, dir string) string {
	t.Helper()
	meta := snapMeta("ceiling", func(m *snap.SnapMeta) {
		m.Marks = []snap.Mark{{Label: "Autopilot: ceiling breached at 110 RPS", ElapsedS: 20, Source: "autopilot"}}
		m.Autopilot = &snap.AutopilotMeta{Mode: "find-ceiling", Outcome: "ceiling_found", CeilingRPS: 95,
			Events: []snap.AutopilotEvent{{State: "stepping", HoldRPS: 95, ElapsedS: 5}}}
	})
	return writeSnap(t, dir, "ceiling", time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC), meta, ep("GET:/a", 500, 80, 0.02, nil))
}

func TestSnapCapture_ReportWritesBesideTheSnapshotAndNamesAutopilot(t *testing.T) {
	dir := snapDirEnv(t)
	path := autopilotSnap(t, dir)

	for _, tc := range []struct{ format, ext, marker string }{
		{"md", ".report.md", "# gg run report: ceiling"},
		{"html", ".report.html", "<title>gg run report: ceiling</title>"},
	} {
		out, err := snapCapture(nil, CaptureInput{Snapshot: path, Report: tc.format})
		if err != nil {
			t.Fatalf("%s: %v", tc.format, err)
		}
		want := strings.TrimSuffix(path, snap.SnapFileExt) + tc.ext
		if out.ReportPath != want {
			t.Fatalf("%s: report_path = %q, want %q", tc.format, out.ReportPath, want)
		}
		body, err := os.ReadFile(want)
		if err != nil || !strings.Contains(string(body), tc.marker) || !strings.Contains(string(body), "ceiling_found") {
			t.Errorf("%s: report file = %v, %.200s", tc.format, err, body)
		}
		if out.Autopilot == nil || out.Autopilot.Outcome != "ceiling_found" || out.Autopilot.CeilingRPS != 95 {
			t.Errorf("%s: autopilot summary = %+v", tc.format, out.Autopilot)
		}
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("expected the snapshot plus two reports, got %d files", len(entries))
	}
}

func TestSnapCapture_NoReportWritesNothing(t *testing.T) {
	dir := snapDirEnv(t)
	path := autopilotSnap(t, dir)
	out, err := snapCapture(nil, CaptureInput{Snapshot: path})
	if err != nil || out.ReportPath != "" {
		t.Fatalf("out = %+v err = %v", out, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("a plain capture must write nothing: %d files", len(entries))
	}
}

func TestSnapCapture_BadReportFormatIsRefusedBeforeAnyWork(t *testing.T) {
	dir := snapDirEnv(t)
	path := autopilotSnap(t, dir)
	_, err := snapCapture(nil, CaptureInput{Snapshot: path, Report: "pdf"})
	if err == nil || !strings.Contains(err.Error(), `"md" or "html"`) {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("nothing may be written: %d files", len(entries))
	}
}

func TestStopSimulation_ReportOption(t *testing.T) {
	dir := t.TempDir()
	snapPath := writeSnap(t, dir, "run", time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC), snapMeta("run", nil), ep("GET:/a", 100, 40, 0, nil))
	t.Setenv(fakegg.SnapEnv, snapPath)
	cs, st := steerClient(t, "normal", nil)

	var bad session.Telemetry
	res := call(t, cs, "stop_simulation", map[string]any{"session_id": st.SessionID, "report": "pdf"}, &bad)
	if !res.IsError || !strings.Contains(errText(res), `"md" or "html"`) {
		t.Fatalf("bad format: isError=%v %q", res.IsError, errText(res))
	}
	var still session.Telemetry
	call(t, cs, "get_live_telemetry", map[string]any{"session_id": st.SessionID}, &still)
	if still.State.Terminal() {
		t.Fatalf("a refused report option must not stop the run: state %s", still.State)
	}

	var tel session.Telemetry
	res = call(t, cs, "stop_simulation", map[string]any{"session_id": st.SessionID, "report": "md"}, &tel)
	if res.IsError || tel.State != session.Stopped {
		t.Fatalf("stop = %+v %s", tel, errText(res))
	}
	want := strings.TrimSuffix(snapPath, snap.SnapFileExt) + ".report.md"
	if tel.ReportPath != want || tel.ReportError != "" {
		t.Fatalf("report_path = %q error = %q, want %q", tel.ReportPath, tel.ReportError, want)
	}
	if body, err := os.ReadFile(want); err != nil || !strings.Contains(string(body), "GET:/a") {
		t.Errorf("report = %v %.100s", err, body)
	}
	if filepath.Dir(tel.ReportPath) != dir {
		t.Errorf("report must sit beside the snapshot: %s", tel.ReportPath)
	}
}

func TestStopSimulation_ReportWithoutASnapshotSaysSoAndStillStops(t *testing.T) {
	t.Setenv(fakegg.SnapEnv, filepath.Join(t.TempDir(), "gone.snap"))
	cs, st := steerClient(t, "normal", nil)
	var tel session.Telemetry
	res := call(t, cs, "stop_simulation", map[string]any{"session_id": st.SessionID, "report": "html"}, &tel)
	if res.IsError || tel.State != session.Stopped || tel.ReportPath != "" || !strings.Contains(tel.ReportError, "no report") {
		t.Fatalf("stop = %+v isError=%v %s", tel, res.IsError, errText(res))
	}
}
