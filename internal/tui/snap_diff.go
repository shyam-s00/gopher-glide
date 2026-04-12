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

// ── styles (module-level so renderDiffXxx helpers can share them) ─────────────

var (
	diffSectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	diffLabelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	diffValueStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	diffPassStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Bold(true)
	diffWarnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD700")).Bold(true)
	diffRegrStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87")).Bold(true)
	diffDimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

// ── model ─────────────────────────────────────────────────────────────────────

type snapDiffModel struct {
	result   snap.DiffResult
	baseInfo snap.SnapInfo
	currInfo snap.SnapInfo
	viewport viewport.Model
	width    int
	height   int
	ready    bool
}

func newSnapDiffModel(result snap.DiffResult, baseInfo, currInfo snap.SnapInfo) snapDiffModel {
	return snapDiffModel{result: result, baseInfo: baseInfo, currInfo: currInfo}
}

// ── Bubble Tea interface ──────────────────────────────────────────────────────

func (m snapDiffModel) Init() tea.Cmd {
	return tea.EnterAltScreen
}

func (m snapDiffModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

func (m snapDiffModel) View() string {
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

func (m snapDiffModel) renderHeader() string {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FAFAFA")).
		Background(lipgloss.Color("#7D56F4")).
		Padding(0, 1).
		MarginTop(1).
		MarginBottom(1)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#63")).
		Padding(0, 2).
		MarginRight(4).
		Width(32)

	baseTag := m.result.Baseline.Tag
	if baseTag == "" || baseTag == "run" {
		baseTag = "(untagged)"
	}
	currTag := m.result.Current.Tag
	if currTag == "" || currTag == "run" {
		currTag = "(untagged)"
	}

	baseBox := lipgloss.JoinVertical(lipgloss.Left,
		diffSectionStyle.Render("BASELINE"),
		diffLabelStyle.Render("Tag:"),
		diffValueStyle.Render(baseTag),
		diffLabelStyle.Render("Date:"),
		diffValueStyle.Render(m.result.Baseline.StartTime.UTC().Format("2006-01-02 15:04 UTC")),
		diffLabelStyle.Render("Requests:"),
		diffValueStyle.Render(svFormatCount(m.result.Baseline.TotalRequests)),
		"",
	)

	currBox := lipgloss.JoinVertical(lipgloss.Left,
		diffSectionStyle.Render("CURRENT"),
		diffLabelStyle.Render("Tag:"),
		diffValueStyle.Render(currTag),
		diffLabelStyle.Render("Date:"),
		diffValueStyle.Render(m.result.Current.StartTime.UTC().Format("2006-01-02 15:04 UTC")),
		diffLabelStyle.Render("Requests:"),
		diffValueStyle.Render(svFormatCount(m.result.Current.TotalRequests)),
		"",
	)

	// Tally verdicts across all endpoints.
	passes, warns, regressions := 0, 0, 0
	for _, ep := range m.result.Endpoints {
		switch ep.Verdict {
		case snap.VerdictPass:
			passes++
		case snap.VerdictWarn:
			warns++
		case snap.VerdictRegression:
			regressions++
		}
	}

	summaryBox := lipgloss.JoinVertical(lipgloss.Left,
		diffSectionStyle.Render("SUMMARY"),
		diffLabelStyle.Render("Endpoints:"),
		diffValueStyle.Render(fmt.Sprintf("%d compared", len(m.result.Endpoints))),
		"",
		diffPassStyle.Render(fmt.Sprintf("  ✓ %d  PASS", passes)),
		diffWarnStyle.Render(fmt.Sprintf("  ⚠ %d  WARN", warns)),
		diffRegrStyle.Render(fmt.Sprintf("  ✗ %d  REGRESSION", regressions)),
		"",
	)

	return lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Gopher Glide (GG) — Snapshot Diff"),
		lipgloss.JoinHorizontal(lipgloss.Top,
			boxStyle.Render(baseBox),
			boxStyle.Render(currBox),
			boxStyle.Render(summaryBox),
		),
	)
}

// ── endpoint diff panels ──────────────────────────────────────────────────────

func (m snapDiffModel) renderEndpoints() string {
	if len(m.result.Endpoints) == 0 {
		return diffDimStyle.Render("  No endpoints to compare.")
	}
	panelWidth := m.viewport.Width - 4
	if panelWidth < 50 {
		panelWidth = 50
	}
	var sb strings.Builder
	for _, ep := range m.result.Endpoints {
		sb.WriteString(renderDiffPanel(ep, panelWidth))
		sb.WriteString("\n")
	}
	return sb.String()
}

func renderDiffPanel(d snap.EndpointDiff, width int) string {
	var body strings.Builder

	// ── title + verdict badge ─────────────────────────────────────────────
	verdictBadge, verdictBorderColor := verdictStyle(d.Verdict)
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CCBBFF")).Render(d.ID)
	body.WriteString(lipgloss.JoinHorizontal(lipgloss.Center, title, "  ", verdictBadge))
	body.WriteString("\n\n")

	// ── only-in-one-snapshot shortcut ─────────────────────────────────────
	if d.BaselineOnly {
		body.WriteString(diffWarnStyle.Render("⬅  Only in baseline — endpoint may have been removed or renamed"))
		return panelBox(body.String(), width, verdictBorderColor)
	}
	if d.CurrentOnly {
		body.WriteString(diffPassStyle.Render("➕ New endpoint in current — not present in baseline"))
		return panelBox(body.String(), width, verdictBorderColor)
	}

	// ── delta sections ────────────────────────────────────────────────────
	colW := (width - 10) / 3
	if colW < 22 {
		colW = 22
	}

	latBlock := renderLatencyDeltaBlock(d.LatencyDelta)
	payBlock := renderPayloadDeltaBlock(d.PayloadDelta)
	errBlock := renderErrorDeltaBlock(d.ErrorRateDelta, d.StatusDelta)

	body.WriteString(lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(colW).MarginRight(2).Render(latBlock),
		lipgloss.NewStyle().Width(colW).MarginRight(2).Render(payBlock),
		lipgloss.NewStyle().Width(colW).Render(errBlock),
	))

	// ── schema changes ────────────────────────────────────────────────────
	if len(d.SchemaChanges) > 0 {
		body.WriteString("\n\n")
		body.WriteString(renderSchemaChanges(d.SchemaChanges))
	}

	return panelBox(body.String(), width, verdictBorderColor)
}

// ── latency delta section ─────────────────────────────────────────────────────

func renderLatencyDeltaBlock(d snap.LatencyDelta) string {
	return lipgloss.JoinVertical(lipgloss.Left,
		diffSectionStyle.Render("Latency Deltas"),
		diffRow("P50:", renderPerfDelta(d.P50PctChange)),
		diffRow("P95:", renderPerfDelta(d.P95PctChange)),
		diffRow("P99:", renderPerfDelta(d.P99PctChange)),
		diffRow("Max:", renderPerfDelta(d.MaxPctChange)),
	)
}

// ── payload size delta section ────────────────────────────────────────────────

func renderPayloadDeltaBlock(d snap.PayloadSizeDelta) string {
	if d.AvgPctChange == 0 && d.P95PctChange == 0 && d.MaxPctChange == 0 {
		return lipgloss.JoinVertical(lipgloss.Left,
			diffSectionStyle.Render("Payload Deltas"),
			diffDimStyle.Render("  no size data"),
		)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		diffSectionStyle.Render("Payload Deltas"),
		diffRow("Avg:", renderSizeDelta(d.AvgPctChange)),
		diffRow("P95:", renderSizeDelta(d.P95PctChange)),
		diffRow("Max:", renderSizeDelta(d.MaxPctChange)),
	)
}

// ── error rate + status delta section ────────────────────────────────────────

func renderErrorDeltaBlock(errDelta float64, statusDelta map[string]float64) string {
	lines := []string{diffSectionStyle.Render("Error & Status")}

	errStr := formatErrRateDelta(errDelta)
	lines = append(lines, diffRow("Error rate:", errStr))

	if len(statusDelta) > 0 {
		lines = append(lines, "")
		// Sort for determinism
		codes := make([]string, 0, len(statusDelta))
		for k := range statusDelta {
			codes = append(codes, k)
		}
		sort.Strings(codes)
		for _, code := range codes {
			delta := statusDelta[code]
			if delta == 0 {
				continue
			}
			lines = append(lines, diffRow(code+":", renderStatusDelta(code, delta)))
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// ── schema changes section ────────────────────────────────────────────────────

func renderSchemaChanges(changes []snap.FieldChange) string {
	var sb strings.Builder
	sb.WriteString(diffSectionStyle.Render("Schema Changes"))
	sb.WriteString("\n")

	for _, c := range changes {
		var prefix string
		var st lipgloss.Style

		switch c.Kind {
		case snap.FieldAdded:
			prefix = "+"
			st = diffPassStyle
		case snap.FieldRemoved:
			prefix = "-"
			st = diffRegrStyle
		case snap.FieldTypeChanged:
			prefix = "~"
			st = diffRegrStyle
		case snap.FieldStabilityChanged:
			prefix = "~"
			st = diffWarnStyle
		}

		detail := schemaChangeDetail(c)
		line := fmt.Sprintf("  %s  %-38s  %s",
			st.Render(prefix),
			diffDimStyle.Render(c.Path),
			diffValueStyle.Render(detail),
		)
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// schemaChangeDetail formats the change description for a single schema field.
func schemaChangeDetail(c snap.FieldChange) string {
	switch c.Kind {
	case snap.FieldAdded:
		return fmt.Sprintf("added  %-8s  %.0f%%  %s",
			c.CurrType, c.CurrPresence*100, c.CurrStability)
	case snap.FieldRemoved:
		return fmt.Sprintf("removed  %-8s  was %.0f%%  %s",
			c.BaseType, c.BasePresence*100, c.BaseStability)
	case snap.FieldTypeChanged:
		return fmt.Sprintf("type  %s → %s", c.BaseType, c.CurrType)
	case snap.FieldStabilityChanged:
		return fmt.Sprintf("stability  %s → %s  (%.0f%% → %.0f%%)",
			c.BaseStability, c.CurrStability,
			c.BasePresence*100, c.CurrPresence*100)
	}
	return ""
}

// ── delta rendering helpers ───────────────────────────────────────────────────

// renderPerfDelta colours a performance % change where positive = regression.
//
//	> +5 %  → red (regression)
//	< −1 %  → green (improvement)
//	otherwise → dim (negligible)
func renderPerfDelta(pct float64) string {
	s := fmtPct(pct)
	switch {
	case pct > 5:
		return diffRegrStyle.Render(s)
	case pct < -1:
		return diffPassStyle.Render(s)
	default:
		return diffDimStyle.Render(s)
	}
}

// renderSizeDelta colours a payload size % change where positive = warn.
//
//	> +10 % → yellow (growing)
//	<  −1 % → green  (shrinking)
//	otherwise → dim (negligible)
func renderSizeDelta(pct float64) string {
	s := fmtPct(pct)
	switch {
	case pct > 10:
		return diffWarnStyle.Render(s)
	case pct < -1:
		return diffPassStyle.Render(s)
	default:
		return diffDimStyle.Render(s)
	}
}

// renderStatusDelta colours the per-status-code distribution change.
// 2xx improvements are green; increases in 4xx/5xx codes are red.
func renderStatusDelta(code string, delta float64) string {
	s := fmtPct(delta * 100) // delta is in [0,1] range → display as pp
	switch {
	case strings.HasPrefix(code, "2") && delta > 0:
		return diffPassStyle.Render(s)
	case (strings.HasPrefix(code, "4") || strings.HasPrefix(code, "5")) && delta > 0:
		return diffRegrStyle.Render(s)
	case delta < 0:
		return diffPassStyle.Render(s)
	default:
		return diffDimStyle.Render(s)
	}
}

func formatErrRateDelta(delta float64) string {
	// Display as percentage points (pp)
	pp := delta * 100
	s := fmt.Sprintf("%+.2f pp", pp)
	switch {
	case pp > 5:
		return diffRegrStyle.Render(s)
	case pp < -1:
		return diffPassStyle.Render(s)
	default:
		return diffDimStyle.Render(s)
	}
}

func fmtPct(pct float64) string {
	if pct > 0 {
		return fmt.Sprintf("+%.1f%%", pct)
	}
	return fmt.Sprintf("%.1f%%", pct)
}

// verdictStyle returns a rendered badge and a border colour for a verdict.
func verdictStyle(v snap.DiffVerdict) (badge string, borderColor lipgloss.Color) {
	badgeStyle := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	switch v {
	case snap.VerdictRegression:
		return badgeStyle.
			Background(lipgloss.Color("#FF5F87")).
			Foreground(lipgloss.Color("#FAFAFA")).
			Render("✗ REGRESSION"), lipgloss.Color("#FF5F87")
	case snap.VerdictWarn:
		return badgeStyle.
			Background(lipgloss.Color("#FFD700")).
			Foreground(lipgloss.Color("#1A1A1A")).
			Render("⚠ WARN"), lipgloss.Color("#FFD700")
	default:
		return badgeStyle.
			Background(lipgloss.Color("#04B575")).
			Foreground(lipgloss.Color("#FAFAFA")).
			Render("✓ PASS"), lipgloss.Color("#04B575")
	}
}

// panelBox wraps content in a rounded border box coloured by verdict.
func panelBox(content string, width int, borderColor lipgloss.Color) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		MarginTop(1).
		Width(width).
		Render(content)
}

// diffRow renders a two-column label + value line.
func diffRow(label, value string) string {
	return fmt.Sprintf("  %s  %s",
		diffLabelStyle.Render(fmt.Sprintf("%-14s", label)),
		value,
	)
}

// ── entry point ───────────────────────────────────────────────────────────────

// StartSnapDiff launches the Bubble Tea snapshot diff TUI.
func StartSnapDiff(result snap.DiffResult, baseInfo, currInfo snap.SnapInfo) error {
	m := newSnapDiffModel(result, baseInfo, currInfo)
	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := p.Run()
	return err
}
