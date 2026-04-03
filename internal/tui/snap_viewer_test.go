package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// ── svFormatCount ─────────────────────────────────────────────────────────────

func TestSvFormatCount(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{1500, "1,500"},
		{999999, "999,999"},
		{1000000, "1,000,000"},
		{1234567, "1,234,567"},
		{2000000, "2,000,000"},
	}
	for _, c := range cases {
		got := svFormatCount(c.n)
		if got != c.want {
			t.Errorf("svFormatCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// ── snapViewModel ─────────────────────────────────────────────────────────────

func newTestSnap() *snap.Snapshot {
	return &snap.Snapshot{
		Version: 1,
		Meta: snap.SnapMeta{
			Tag:           "v1.0.0",
			TotalRequests: 5000,
			PeakRPS:       100,
		},
		Endpoints: []snap.EndpointSnap{
			{
				ID:           "GET:/api/users",
				StatusDist:   map[string]float64{"200": 0.97, "500": 0.03},
				Latency:      snap.LatencyStats{P50: 12.3, P95: 48.1, P99: 120.4, Max: 300},
				ErrorRate:    0.03,
				RequestCount: 5000,
			},
		},
	}
}

func TestSnapViewModel_Init(t *testing.T) {
	s := newTestSnap()
	m := newSnapViewModel(s, snap.SnapInfo{FileName: "v1.0.0-20260316-143022.snap"})
	cmd := m.Init()
	if cmd == nil {
		t.Error("Init should return a non-nil tea.Cmd")
	}
}

func TestSnapViewModel_View_NotReady(t *testing.T) {
	s := newTestSnap()
	m := newSnapViewModel(s, snap.SnapInfo{})
	out := m.View()
	if out == "" {
		t.Error("View should return non-empty loading string when not ready")
	}
}

func TestSnapViewModel_View_Ready(t *testing.T) {
	s := newTestSnap()
	m := newSnapViewModel(s, snap.SnapInfo{FileName: "v1.0.0-20260316-143022.snap"})
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	out := m2.(snapViewModel).View()
	if out == "" {
		t.Error("View should return non-empty string when ready")
	}
}

func TestSnapViewModel_Update_QuitKey(t *testing.T) {
	s := newTestSnap()
	m := newSnapViewModel(s, snap.SnapInfo{})
	for _, key := range []string{"q", "ctrl+c", "esc"} {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if cmd == nil {
			t.Errorf("key %q: expected a quit cmd, got nil", key)
		}
	}
}

func TestSnapViewModel_Update_WindowSize(t *testing.T) {
	s := newTestSnap()
	m := newSnapViewModel(s, snap.SnapInfo{})
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 180, Height: 50})
	got := m2.(snapViewModel)
	if got.width != 180 {
		t.Errorf("width: want 180, got %d", got.width)
	}
	if got.height != 50 {
		t.Errorf("height: want 50, got %d", got.height)
	}
	if !got.ready {
		t.Error("ready should be true after first WindowSizeMsg")
	}
}

func TestSnapViewModel_Update_WindowSize_Resize(t *testing.T) {
	// Second WindowSizeMsg (already ready) should update viewport dimensions.
	s := newTestSnap()
	m := newSnapViewModel(s, snap.SnapInfo{})
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 180, Height: 50})
	m3, _ := m2.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	got := m3.(snapViewModel)
	if got.width != 200 {
		t.Errorf("width after resize: want 200, got %d", got.width)
	}
}

func TestSnapViewModel_RenderHeader_UntaggedFallback(t *testing.T) {
	s := &snap.Snapshot{Version: 1, Meta: snap.SnapMeta{Tag: ""}}
	m := newSnapViewModel(s, snap.SnapInfo{})
	h := m.renderHeader()
	if h == "" {
		t.Error("renderHeader should return non-empty string")
	}
}

func TestSnapViewModel_RenderHeader_RunTag(t *testing.T) {
	s := &snap.Snapshot{Version: 1, Meta: snap.SnapMeta{Tag: "run"}}
	m := newSnapViewModel(s, snap.SnapInfo{})
	h := m.renderHeader()
	if h == "" {
		t.Error("renderHeader with 'run' tag should return non-empty string")
	}
}

func TestSnapViewModel_RenderHeader_LongConfigHash(t *testing.T) {
	s := &snap.Snapshot{
		Version: 1,
		Meta:    snap.SnapMeta{Tag: "v1", ConfigHash: "sha256:abcdefabcdefabcdefabcdefabcdefabcdef"},
	}
	m := newSnapViewModel(s, snap.SnapInfo{})
	h := m.renderHeader()
	if h == "" {
		t.Error("renderHeader with long config hash should return non-empty string")
	}
}

func TestSnapViewModel_RenderEndpoints_Empty(t *testing.T) {
	s := &snap.Snapshot{Version: 1, Endpoints: nil}
	m := newSnapViewModel(s, snap.SnapInfo{})
	out := m.renderEndpoints()
	if out == "" {
		t.Error("renderEndpoints with no endpoints should return non-empty string")
	}
}

func TestSnapViewModel_RenderEndpoints_WithData(t *testing.T) {
	s := newTestSnap()
	m := newSnapViewModel(s, snap.SnapInfo{})
	out := m.renderEndpoints()
	if out == "" {
		t.Error("renderEndpoints with endpoints should return non-empty string")
	}
}

func TestRenderEndpointPanel_WithSchema(t *testing.T) {
	ep := snap.EndpointSnap{
		ID:         "GET:/api/users",
		StatusDist: map[string]float64{"200": 0.95, "404": 0.03, "500": 0.02},
		Latency:    snap.LatencyStats{P50: 10, P95: 50, P99: 100, Max: 300},
		ErrorRate:  0.05,
		Schema: &snap.SchemaSnapshot{
			SampleCount: 50,
			Fields: map[string]snap.FieldSchema{
				"id":    {Type: "string", Presence: 1.0, Stability: "STABLE"},
				"email": {Type: "string", Presence: 0.5, Stability: "VOLATILE"},
				"meta":  {Type: "object", Presence: 0.1, Stability: "RARE"},
			},
		},
	}
	out := renderEndpointPanel(ep, 120)
	if out == "" {
		t.Error("renderEndpointPanel with schema should return non-empty string")
	}
}

func TestRenderEndpointPanel_LowErrorRate(t *testing.T) {
	// error rate < 1% → successStyle
	ep := snap.EndpointSnap{
		ID:         "GET:/api/health",
		StatusDist: map[string]float64{"200": 1.0},
		ErrorRate:  0.005,
	}
	out := renderEndpointPanel(ep, 120)
	if out == "" {
		t.Error("renderEndpointPanel with low error rate should return non-empty string")
	}
}

func TestRenderEndpointPanel_MedErrorRate(t *testing.T) {
	// error rate 1–5% → warnStyle
	ep := snap.EndpointSnap{
		ID:         "GET:/api/widget",
		StatusDist: map[string]float64{"200": 0.98, "500": 0.02},
		ErrorRate:  0.02,
	}
	out := renderEndpointPanel(ep, 120)
	if out == "" {
		t.Error("renderEndpointPanel with medium error rate should return non-empty string")
	}
}

func TestRenderEndpointPanel_3xxStatus(t *testing.T) {
	// 3xx status code → valueStyle (default branch)
	ep := snap.EndpointSnap{
		ID:         "GET:/redirect",
		StatusDist: map[string]float64{"301": 1.0},
		ErrorRate:  0,
	}
	out := renderEndpointPanel(ep, 120)
	if out == "" {
		t.Error("renderEndpointPanel with 3xx status should return non-empty string")
	}
}
