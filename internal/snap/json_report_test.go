package snap

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRefOf(t *testing.T) {
	d := time.Date(2026, 11, 2, 9, 30, 0, 0, time.FixedZone("x", 3600))
	r := RefOf(SnapInfo{ID: 3, Tag: "v1", Date: d, Path: "/s/v1.snap", FileName: "v1.snap"})
	if r.ID != 3 || r.Tag != "v1" || r.Date != "2026-11-02T08:30:00Z" || r.Path != "/s/v1.snap" || r.File != "v1.snap" {
		t.Errorf("ref = %+v", r)
	}
	if r := RefOf(SnapInfo{Path: "/x/a.snap", FileName: "a.snap"}); r.Date != "" || r.ID != 0 {
		t.Errorf("a file given by path has no id or date: %+v", r)
	}
}

func TestBuildListReport(t *testing.T) {
	sums := []SnapSummary{
		{SnapInfo: SnapInfo{ID: 1, Tag: "a", Date: time.Unix(0, 0), Path: "/d/a.snap", FileName: "a.snap"}, TotalRequests: 10, PeakRPS: 5, EndpointCount: 2},
		{SnapInfo: SnapInfo{ID: 2, Tag: "b", Path: "/d/b.snap", FileName: "b.snap"}},
	}
	js, err := MarshalReport(BuildListReport("/d", sums))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		SnapDir   string `json:"snap_dir"`
		Snapshots []struct {
			ID            int    `json:"id"`
			Tag           string `json:"tag"`
			TotalRequests int64  `json:"total_requests"`
			PeakRPS       int    `json:"peak_rps"`
			Endpoints     int    `json:"endpoints"`
			Path          string `json:"path"`
		} `json:"snapshots"`
	}
	if err := json.Unmarshal([]byte(js), &got); err != nil {
		t.Fatal(err)
	}
	if got.SnapDir != "/d" || len(got.Snapshots) != 2 || got.Snapshots[0].ID != 1 || got.Snapshots[0].TotalRequests != 10 ||
		got.Snapshots[0].PeakRPS != 5 || got.Snapshots[0].Endpoints != 2 || got.Snapshots[1].Tag != "b" {
		t.Errorf("report = %s", js)
	}
}

func TestBuildListReport_EmptyIsAnEmptyArray(t *testing.T) {
	js, _ := MarshalReport(BuildListReport("/d", nil))
	if !strings.Contains(js, `"snapshots": []`) {
		t.Errorf("want an empty array, got %s", js)
	}
}

func TestInfoFromPath(t *testing.T) {
	got := InfoFromPath("/x/y/release-1-20261102-093000.snap")
	if got.Tag != "release-1" || got.FileName != "release-1-20261102-093000.snap" || got.Path != "/x/y/release-1-20261102-093000.snap" ||
		!got.Date.Equal(time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("standard name = %+v", got)
	}
	odd := InfoFromPath("/x/y/mine.snap")
	if odd.Tag != "" || odd.Path != "/x/y/mine.snap" || odd.FileName != "mine.snap" || !odd.Date.IsZero() {
		t.Errorf("non-standard name = %+v", odd)
	}
}
