package mcp

import (
	"fmt"
	"os"
	"strings"

	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// parseReportOption checks a tool's report argument; empty means no report.
func parseReportOption(v string) (snap.ReportFormat, bool, error) {
	if v == "" {
		return "", false, nil
	}
	f, err := snap.ParseReportFormat(v)
	if err != nil {
		return "", false, fmt.Errorf("report must be \"md\" or \"html\", got %q", v)
	}
	return f, true, nil
}

// writeReport renders the snapshot at snapPath into a file beside it and
// returns that file's path. The report holds only what the snapshot holds.
func writeReport(s *snap.Snapshot, snapPath string, f snap.ReportFormat) (string, error) {
	title := s.Meta.Tag
	base := strings.TrimSuffix(snapPath, snap.SnapFileExt)
	if title == "" {
		title = base[strings.LastIndexAny(base, `/\`)+1:]
	}
	body, err := snap.RenderReport(s, title, f)
	if err != nil {
		return "", err
	}
	out := base + ".report" + f.Ext()
	if err := os.WriteFile(out, body, 0o600); err != nil {
		return "", fmt.Errorf("write report: %w", fsReason(err))
	}
	return out, nil
}

// AutopilotSummary is what a snapshot recorded of the built-in autopilot.
type AutopilotSummary struct {
	Mode       string `json:"mode"`
	Outcome    string `json:"outcome"`
	CeilingRPS int    `json:"ceiling_rps,omitempty"`
}

func autopilotSummary(s *snap.Snapshot) *AutopilotSummary {
	a := s.Meta.Autopilot
	if a == nil {
		return nil
	}
	return &AutopilotSummary{Mode: a.Mode, Outcome: a.Outcome, CeilingRPS: a.CeilingRPS}
}
