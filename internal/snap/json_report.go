package snap

import (
	"encoding/json"
	"time"
)

// Ref identifies one snapshot file in a JSON report.
type Ref struct {
	ID   int    `json:"id,omitempty"` // 1-based position in the directory listing; 0 for a file given by path
	Tag  string `json:"tag"`
	Date string `json:"date,omitempty"` // RFC 3339, UTC
	Path string `json:"path"`
	File string `json:"file"`
}

// RefOf describes a resolved snapshot.
func RefOf(info SnapInfo) Ref {
	r := Ref{ID: info.ID, Tag: info.Tag, Path: info.Path, File: info.FileName}
	if !info.Date.IsZero() {
		r.Date = info.Date.UTC().Format(time.RFC3339)
	}
	return r
}

// ListEntry is one snapshot in a ListReport.
type ListEntry struct {
	Ref
	TotalRequests int64 `json:"total_requests"`
	PeakRPS       int   `json:"peak_rps"`
	Endpoints     int   `json:"endpoints"`
}

// ListReport is the output of `gg snap list --reporter json`.
type ListReport struct {
	SnapDir   string      `json:"snap_dir"`
	Snapshots []ListEntry `json:"snapshots"` // oldest first, so ids match the text listing
}

// BuildListReport converts a directory listing into its JSON form.
func BuildListReport(dir string, sums []SnapSummary) ListReport {
	r := ListReport{SnapDir: dir, Snapshots: make([]ListEntry, 0, len(sums))}
	for _, s := range sums {
		r.Snapshots = append(r.Snapshots, ListEntry{
			Ref: RefOf(s.SnapInfo), TotalRequests: s.TotalRequests, PeakRPS: s.PeakRPS, Endpoints: s.EndpointCount,
		})
	}
	return r
}

// DiffReport is the output of `gg snap diff --reporter json`.
type DiffReport struct {
	BaselineRef Ref        `json:"baseline_ref"`
	CurrentRef  Ref        `json:"current_ref"`
	Result      DiffResult `json:"result"`
}

// ViewReport is the output of `gg snap view --reporter json`.
type ViewReport struct {
	Ref      Ref       `json:"ref"`
	Snapshot *Snapshot `json:"snapshot"`
}

// MarshalReport renders any report as indented JSON.
func MarshalReport(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
