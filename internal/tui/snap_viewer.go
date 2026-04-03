package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

// ── model ─────────────────────────────────────────────────────────────────────

type snapViewModel struct {
	snap     *snap.Snapshot
	info     snap.SnapInfo
	viewport viewport.Model
	width    int
	height   int
	ready    bool
}

func newSnapViewModel(s *snap.Snapshot, info snap.SnapInfo) snapViewModel {
	return snapViewModel{snap: s, info: info}
}

// ── Bubble Tea interface ──────────────────────────────────────────────────────

func (m snapViewModel) Init() tea.Cmd {
	return tea.EnterAltScreen
}

func (m snapViewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		headerH := lipgloss.Height(m.renderHeader())
		hintsH := 1
		vpH := m.height - headerH - hintsH - 1
		if vpH < 5 {
			vpH = 5
		}
		vpW := m.width - 2
		if !m.ready {
			m.viewport = viewport.New(vpW, vpH)
			m.ready = true
		} else {
			m.viewport.Width = vpW
			m.viewport.Height = vpH
		}
		m.viewport.SetContent(m.renderEndpoints())
	}
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m snapViewModel) View() string {
	if !m.ready {
		return "\n  Loading..."
	}
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	pct := int(m.viewport.ScrollPercent() * 100)
	hints := hintStyle.Render(fmt.Sprintf(
		"[↑/k] scroll up   [↓/j] scroll down   [q] quit   %d%%", pct,
	))
	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderHeader(),
		m.viewport.View(),
		hints,
	)
}

// ── header ────────────────────────────────────────────────────────────────────

func (m snapViewModel) renderHeader() string {
	s := m.snap
	duration := s.Meta.EndTime.Sub(s.Meta.StartTime)

	tag := s.Meta.Tag
	if tag == "" || tag == "run" {
		tag = "(untagged)"
	}
	configHash := s.Meta.ConfigHash
	if len(configHash) > 22 {
		configHash = configHash[:22] + "…"
	}

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FAFAFA")).
		Background(lipgloss.Color("#7D56F4")).
		Padding(0, 1).
		MarginTop(1).
		MarginBottom(1)

	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#63")).
		Padding(0, 2).
		MarginRight(4).
		Width(28)

	metaBox := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("SNAPSHOT"),
		labelStyle.Render("Tag:"),
		valueStyle.Render(tag),
		labelStyle.Render("Date:"),
		valueStyle.Render(s.Meta.StartTime.UTC().Format("2006-01-02 15:04 UTC")),
		labelStyle.Render("Duration:"),
		valueStyle.Render(formatDuration(duration)),
		"",
	)

	perfBox := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("PERFORMANCE"),
		labelStyle.Render("Total Requests:"),
		valueStyle.Render(svFormatCount(s.Meta.TotalRequests)),
		labelStyle.Render("Peak RPS:"),
		valueStyle.Render(fmt.Sprintf("%d", s.Meta.PeakRPS)),
		labelStyle.Render("Endpoints:"),
		valueStyle.Render(fmt.Sprintf("%d", len(s.Endpoints))),
		"",
	)

	fileBox := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("FILE"),
		labelStyle.Render("Name:"),
		valueStyle.Render(m.info.FileName),
		labelStyle.Render("Config Hash:"),
		valueStyle.Render(configHash),
		"",
	)

	return lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Gopher Glide (GG) — Snapshot Viewer"),
		lipgloss.JoinHorizontal(lipgloss.Top,
			boxStyle.Render(metaBox),
			boxStyle.Render(perfBox),
			boxStyle.Render(fileBox),
		),
	)
}

// ── endpoint panels ───────────────────────────────────────────────────────────

func (m snapViewModel) renderEndpoints() string {
	if len(m.snap.Endpoints) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("241")).
			Render("  No endpoint data recorded.")
	}
	panelWidth := m.viewport.Width - 4
	if panelWidth < 40 {
		panelWidth = 40
	}
	var sb strings.Builder
	for _, ep := range m.snap.Endpoints {
		sb.WriteString(renderEndpointPanel(ep, panelWidth))
		sb.WriteString("\n")
	}
	return sb.String()
}

func renderEndpointPanel(ep snap.EndpointSnap, width int) string {
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Bold(true)
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87")).Bold(true)
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD700")).Bold(true)

	// Status distribution
	codes := make([]string, 0, len(ep.StatusDist))
	for code := range ep.StatusDist {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	statusLines := []string{sectionStyle.Render("Status Distribution")}
	for _, code := range codes {
		pct := ep.StatusDist[code] * 100
		var st lipgloss.Style
		switch {
		case strings.HasPrefix(code, "2"):
			st = successStyle
		case strings.HasPrefix(code, "4"), strings.HasPrefix(code, "5"):
			st = errorStyle
		default:
			st = valueStyle
		}
		statusLines = append(statusLines, fmt.Sprintf("  %s  %s",
			labelStyle.Render(fmt.Sprintf("%-6s", code+":")),
			st.Render(fmt.Sprintf("%.1f%%", pct)),
		))
	}
	statusBlock := strings.Join(statusLines, "\n")

	// Latency
	latBlock := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("Latency"),
		fmt.Sprintf("%s  %s", labelStyle.Render("P50:"), valueStyle.Render(fmt.Sprintf("%.1f ms", ep.Latency.P50))),
		fmt.Sprintf("%s  %s", labelStyle.Render("P95:"), valueStyle.Render(fmt.Sprintf("%.1f ms", ep.Latency.P95))),
		fmt.Sprintf("%s  %s", labelStyle.Render("P99:"), valueStyle.Render(fmt.Sprintf("%.1f ms", ep.Latency.P99))),
		fmt.Sprintf("%s  %s", labelStyle.Render("Max:"), valueStyle.Render(fmt.Sprintf("%.1f ms", ep.Latency.Max))),
	)

	// Error rate + request count
	erStyle := successStyle
	if ep.ErrorRate >= 0.05 {
		erStyle = errorStyle
	} else if ep.ErrorRate >= 0.01 {
		erStyle = warnStyle
	}
	statsBlock := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("Stats"),
		fmt.Sprintf("%s  %s", labelStyle.Render("Error Rate:"), erStyle.Render(fmt.Sprintf("%.2f%%", ep.ErrorRate*100))),
		fmt.Sprintf("%s  %s", labelStyle.Render("Requests:  "), valueStyle.Render(svFormatCount(ep.RequestCount))),
	)

	colWidth := (width - 10) / 3
	if colWidth < 20 {
		colWidth = 20
	}
	topRow := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(colWidth).MarginRight(2).Render(statusBlock),
		lipgloss.NewStyle().Width(colWidth).MarginRight(2).Render(latBlock),
		lipgloss.NewStyle().Width(colWidth).Render(statsBlock),
	)

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CCBBFF"))
	var body strings.Builder
	body.WriteString(titleStyle.Render(ep.ID))
	body.WriteString("\n\n")
	body.WriteString(topRow)

	// Schema fields
	if ep.Schema != nil && len(ep.Schema.Fields) > 0 {
		body.WriteString("\n\n")
		body.WriteString(sectionStyle.Render("Schema Fields"))
		body.WriteString("\n")

		fieldNames := make([]string, 0, len(ep.Schema.Fields))
		for name := range ep.Schema.Fields {
			fieldNames = append(fieldNames, name)
		}
		sort.Strings(fieldNames)

		for _, name := range fieldNames {
			f := ep.Schema.Fields[name]
			var stabStyle lipgloss.Style
			switch f.Stability {
			case snap.StabilityStable:
				stabStyle = successStyle
			case snap.StabilityVolatile:
				stabStyle = warnStyle
			default:
				stabStyle = labelStyle
			}
			body.WriteString(fmt.Sprintf("  %s  %s  %5.1f%%  %s\n",
				labelStyle.Render(fmt.Sprintf("%-38s", name)),
				valueStyle.Render(fmt.Sprintf("%-8s", f.Type)),
				f.Presence*100,
				stabStyle.Render(f.Stability),
			))
		}
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#63")).
		Padding(0, 1).
		MarginTop(1).
		Width(width).
		Render(body.String())
}

// ── helpers ───────────────────────────────────────────────────────────────────

func svFormatCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%d,%03d,%03d", n/1_000_000, (n/1_000)%1_000, n%1_000)
	case n >= 1_000:
		return fmt.Sprintf("%d,%03d", n/1_000, n%1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// ── entry point ───────────────────────────────────────────────────────────────

// StartSnapViewer launches the Bubble Tea snapshot viewer TUI.
func StartSnapViewer(s *snap.Snapshot, info snap.SnapInfo) error {
	m := newSnapViewModel(s, info)
	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := p.Run()
	return err
}
