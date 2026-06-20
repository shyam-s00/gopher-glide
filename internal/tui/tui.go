package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── constants ─────────────────────────────────────────────────────────────────

const (
	// historyInterval is the time resolution of the RPS history.
	// One sample is recorded per interval regardless of terminal width.
	historyInterval = 500 * time.Millisecond

	yAxisWidth  = 5
	chartHeight = 10
	minWidth    = 60

	// defaultTickInterval is the redraw cadence used when no --tui-tick-ms
	// override is supplied — ~24fps, matching the rate set in commit af96369.
	defaultTickInterval = 42 * time.Millisecond

	// minTickInterval/maxTickInterval bound an explicit --tui-tick-ms
	// override. Below ~16ms (60fps) a terminal can't render any faster
	// anyway, so anything past that floor is wasted CPU; above 100ms the
	// UI starts to feel unresponsive, so there's no reason to go slower.
	minTickInterval = 16 * time.Millisecond
	maxTickInterval = 100 * time.Millisecond
)

// ── model ─────────────────────────────────────────────────────────────────────

type model struct {
	engine     engine.Runner
	config     *config.Config
	specs      []httpreader.RequestSpec
	ctx        context.Context
	cancel     context.CancelFunc
	engineDone chan struct{} // closed by Init's goroutine once RunStages returns
	logView    viewport.Model
	spinner    spinner.Model

	width  int
	height int
	ready  bool

	// tickInterval is the configured --tui-tick-ms override (0 = use
	// defaultTickInterval). Read through effectiveTickInterval(), which
	// clamps to [minTickInterval, maxTickInterval].
	tickInterval time.Duration

	running      bool
	showFailures bool
	metrics      *engine.MetricsSnapshot

	currentStage   int
	stageStartTime time.Time
	stageElapsed   time.Duration

	// rpsHistory[slot] = actual RPS recorded at that time-slot.
	// slot = elapsed / historyInterval — completely independent of terminal width.
	rpsHistory []float64
	runStart   time.Time

	// director mode feedback
	directorMsg     string
	directorMsgTime time.Time

	// snap indicator — set from Start() when --snap is active
	snapping   bool
	snapDir    string
	snapStatus string // set once the post-run callback completes

	droppedBannerShown bool // tracks when the 'Server Saturated' banner appears to trigger a layout recompute

	// onRunComplete is dispatched as a background tea.Cmd exactly once when
	// the engine stops naturally. It must not write to stdout/stderr.
	// Returns a short status string shown in the director bar.
	// nil when no post-run work is needed.
	onRunComplete func() string
}

type tickMsg time.Time

// snapFinalizedMsg is sent back into the model once the post-run callback
// completes in its background goroutine.
type snapFinalizedMsg struct{ status string }

// ── init ──────────────────────────────────────────────────────────────────────

func initialModel(eng engine.Runner, cfg *config.Config, specs []httpreader.RequestSpec) model {
	ctx, cancel := context.WithCancel(context.Background())
	vp := viewport.New(0, 0)
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(macchiato.Success)
	return model{
		engine:       eng,
		config:       cfg,
		specs:        specs,
		ctx:          ctx,
		cancel:       cancel,
		engineDone:   make(chan struct{}),
		logView:      vp,
		spinner:      sp,
		metrics:      &engine.MetricsSnapshot{},
		showFailures: true,
		rpsHistory:   make([]float64, 0, 256),
	}
}

func (m model) Init() tea.Cmd {
	go func() {
		_ = m.engine.RunStages(m.ctx, m.config, m.specs)
		close(m.engineDone) // unblocks Start() so it can return only after the engine is fully stopped
	}()
	return tea.Batch(m.tickCmd(), m.spinner.Tick, tea.EnterAltScreen)
}

// effectiveTickInterval returns the redraw cadence to use, clamping any
// explicit --tui-tick-ms override into [minTickInterval, maxTickInterval].
func (m model) effectiveTickInterval() time.Duration {
	if m.tickInterval <= 0 {
		return defaultTickInterval
	}
	if m.tickInterval < minTickInterval {
		return minTickInterval
	}
	if m.tickInterval > maxTickInterval {
		return maxTickInterval
	}
	return m.tickInterval
}

func (m model) tickCmd() tea.Cmd {
	return tea.Tick(m.effectiveTickInterval(), func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// ── layout ────────────────────────────────────────────────────────────────────

type layout struct {
	chartWidth int
	logHeight  int
	logWidth   int
}

// yAxisWidth returns the width reserved for the y-axis label column,
// including its trailing space. The default (5) fits up to 4-digit RPS
// values (e.g. " 9999 "); plans with a peak RPS of 10000 or more need an
// extra column per additional digit, otherwise the axis labels overflow
// their reserved column and the chart grid shifts out of alignment.
func (m model) yAxisWidth() int {
	peak := 0
	if m.config != nil {
		peak = m.config.PeakRPS()
	}
	w := len(fmt.Sprintf("%d", peak)) + 1
	if w < yAxisWidth {
		w = yAxisWidth
	}
	return w
}

func (m model) computeChartWidth() int {
	w := m.width
	if w < minWidth {
		w = minWidth
	}
	cw := w - m.yAxisWidth() - 1 - 6
	if cw < 20 {
		cw = 20
	}
	return cw
}

func (m model) computeLayout() layout {
	// Bare/test models (ready=false, nil engine) skip rendering the fixed
	// sections entirely; computeLayoutForHeights falls back to the static
	// estimate in that case.
	var headerH, timelineH int
	if m.ready {
		headerH = lipgloss.Height(m.renderHeader())
		timelineH = lipgloss.Height(m.renderTimeline())
	}
	return m.computeLayoutForHeights(headerH, timelineH)
}

// computeLayoutForHeights derives the layout from the already-rendered
// heights of the header and timeline sections, avoiding a redundant render
// of those (expensive) sections purely for measurement purposes.
//
// When the model is fully initialised (engine + config present), the actual
// rendered heights of the fixed sections are used so that logH fills the
// remaining terminal rows exactly — regardless of journey-mode panel counts,
// label wrapping, or any other rendering quirk.
//
// Bare/test models (ready=false, nil engine) fall back to the static
// estimate so that geometry unit tests remain valid without a live engine.
func (m model) computeLayoutForHeights(headerH, timelineH int) layout {
	w := m.width
	if w < minWidth {
		w = minWidth
	}
	const fallbackUsed = 41 // header(22) + timeline(16) + hintBar(1) + logBorder(2)
	// The "Server Saturated" banner (rendered in View when DroppedCount > 0)
	// adds 3 lines (MarginTop + content + MarginBottom). Without subtracting
	// it here, total rendered height exceeds the terminal height and the
	// alt-screen scrolls the title bar off the top.
	bannerH := 0
	if m.metrics != nil && m.metrics.DroppedCount > 0 {
		bannerH = 3
	}
	var logH int
	if m.ready {
		logH = m.height - headerH - timelineH - 1 - 2 - bannerH
	} else {
		logH = m.height - fallbackUsed - bannerH
	}
	if logH < 3 {
		logH = 3
	}
	return layout{
		chartWidth: m.computeChartWidth(),
		logHeight:  logH,
		logWidth:   w - 4,
	}
}

// ── time ↔ column helpers ─────────────────────────────────────────────────────

func timeToCol(t, total time.Duration, chartWidth int) int {
	if total <= 0 || chartWidth <= 0 {
		return 0
	}
	c := int(float64(chartWidth) * float64(t) / float64(total))
	if c < 0 {
		c = 0
	}
	if c >= chartWidth {
		c = chartWidth - 1
	}
	return c
}

func slotToCol(slot, totalSlots, chartWidth int) int {
	if totalSlots <= 0 || chartWidth <= 0 {
		return 0
	}
	c := int(float64(chartWidth) * float64(slot) / float64(totalSlots))
	if c >= chartWidth {
		c = chartWidth - 1
	}
	return c
}

// ── renderHeader ──────────────────────────────────────────────────────────────

func (m model) renderHeader() string {
	elapsed := m.engine.GetElapsedTime()

	statusStr := "STOPPED"
	statusStyle := styles.ErrorBold
	if m.running {
		statusStr = "RUNNING"
		statusStyle = styles.SuccessBold
	}
	statusLine := statusStyle.Render(statusStr)
	if m.running {
		statusLine = statusStyle.Render(statusStr) + " " + m.spinner.View()
	}

	stages := m.config.Stages
	stageIdx := m.currentStage
	if stageIdx >= len(stages) {
		stageIdx = len(stages) - 1
	}
	prevRPS := 0
	if stageIdx > 0 {
		prevRPS = stages[stageIdx-1].TargetRPS
	}
	stageLabel := "–"
	if len(stages) > 0 {
		stageLabel = fmt.Sprintf("[%d/%d] %s", stageIdx+1, len(stages), stages[stageIdx].Label(prevRPS))
	}

	profileStr := "custom"
	if p := m.config.ConfigSection.ProfileName; p != "" {
		profileStr = p
	}

	jitterVal := m.config.ConfigSection.Jitter
	jitterStr := "off"
	if jitterVal > 0 {
		jitterStr = fmt.Sprintf("±%.0f%%", jitterVal*100)
	}

	// Always 4 panels: CONFIG | LOAD | RESULTS/NETWORK | LATENCY.
	// Each panel occupies: border(2) + padding(2) + gap = panelChrome+panelGap chars.
	const panelGap = 2
	const panelChrome = 4
	const numPanels = 4

	w := m.width
	if w < minWidth {
		w = minWidth
	}
	contentWidth := (w - numPanels*(panelChrome+panelGap)) / numPanels
	if contentWidth < 18 {
		contentWidth = 18
	}
	kpiPanel := styles.PanelBorder.Width(contentWidth).MarginRight(panelGap)

	lbl := func(s string) string { return styles.MetricLabel.Render(s) }
	val := func(s string) string { return styles.MetricValue.Render(s) }
	// kv renders label and value inline on a single row.
	kv := func(label, value string) string { return lbl(label+" ") + val(value) }
	// kvr renders label inline with a pre-styled value string.
	kvr := func(label, rendered string) string { return lbl(label+" ") + rendered }

	configRows := []string{
		styles.SectionTitle.Render("CONFIGURATION"),
		kvr("Status:", statusLine),
		kv("Uptime:", fmt.Sprintf("%.2fs", elapsed)),
		kv("Http File:", m.config.ConfigSection.HTTPFile),
		kv("Profile:", profileStr),
		kv("Stage:", stageLabel),
	}

	// Journey mode splits into LOAD PROFILE (iterations) + NETWORK (HTTP).
	// Standard mode splits into LOAD (rps targets) + RESULTS (counts/errors).
	var middleRowSets [][]string
	if m.metrics.IsJourneyMode {
		middleRowSets = [][]string{
			{
				styles.SectionTitle.Render("LOAD"),
				kv("Target Iter/s:", fmt.Sprintf("%d", m.metrics.TargetRPS)),
				kv("Active Actors:", fmt.Sprintf("%d", m.metrics.ActiveVPUs)),
				kv("Jitter:", jitterStr),
			},
			{
				styles.SectionTitle.Render("RESULTS"),
				kv("HTTP RPS:", fmt.Sprintf("%.2f", m.metrics.Throughput)),
				kv("Total Requests:", fmt.Sprintf("%d", m.metrics.TotalRequests)),
				kvr("Success:", styles.SuccessBold.Render(fmt.Sprintf("%d", m.metrics.SuccessCount))),
				kvr("Failed:", styles.ErrorBold.Render(fmt.Sprintf("%d", m.metrics.FailureCount))),
				kvr("Dropped:", styles.Highlight.Render(fmt.Sprintf("%d", m.metrics.DroppedCount))),
				kv("Error Rate:", fmt.Sprintf("%.2f%%", m.metrics.ErrorRate*100)),
			},
		}
	} else {
		middleRowSets = [][]string{
			{
				styles.SectionTitle.Render("LOAD"),
				kv("Active Actors:", fmt.Sprintf("%d", m.metrics.ActiveVPUs)),
				kv("Target RPS:", fmt.Sprintf("%d", m.metrics.TargetRPS)),
				kv("Actual RPS:", fmt.Sprintf("%.2f", m.metrics.Throughput)),
				kv("Jitter:", jitterStr),
			},
			{
				styles.SectionTitle.Render("RESULTS"),
				kv("Total Requests:", fmt.Sprintf("%d", m.metrics.TotalRequests)),
				kvr("Success:", styles.SuccessBold.Render(fmt.Sprintf("%d", m.metrics.SuccessCount))),
				kvr("Failed:", styles.ErrorBold.Render(fmt.Sprintf("%d", m.metrics.FailureCount))),
				kvr("Dropped:", styles.Highlight.Render(fmt.Sprintf("%d", m.metrics.DroppedCount))),
				kv("Error Rate:", fmt.Sprintf("%.2f%%", m.metrics.ErrorRate*100)),
			},
		}
	}

	latencyRows := []string{
		styles.SectionTitle.Render("LATENCY"),
		kv("Min:", fmt.Sprintf("%.2fms", m.metrics.MinLatency)),
		kv("Max:", fmt.Sprintf("%.2fms", m.metrics.MaxLatency)),
		kv("P50:", fmt.Sprintf("%.2fms", m.metrics.P50Latency)),
		kv("P95:", fmt.Sprintf("%.2fms", m.metrics.P95Latency)),
		kv("P99:", fmt.Sprintf("%.2fms", m.metrics.P99Latency)),
	}

	maxRows := len(configRows)
	for _, rs := range middleRowSets {
		if len(rs) > maxRows {
			maxRows = len(rs)
		}
	}
	if len(latencyRows) > maxRows {
		maxRows = len(latencyRows)
	}

	renderPanel := func(rows []string) string {
		padded := make([]string, maxRows)
		copy(padded, rows)
		return kpiPanel.Render(lipgloss.JoinVertical(lipgloss.Left, padded...))
	}

	panels := []string{renderPanel(configRows)}
	for _, rs := range middleRowSets {
		panels = append(panels, renderPanel(rs))
	}
	panels = append(panels, renderPanel(latencyRows))

	return lipgloss.JoinVertical(lipgloss.Left,
		styles.TitleBar.Render("Gopher Glide (GG)"),
		lipgloss.JoinHorizontal(lipgloss.Top, panels...),
	)
}

// ── renderTimeline ────────────────────────────────────────────────────────────

func (m model) renderTimeline() string {
	stages := m.config.Stages
	if len(stages) == 0 {
		return ""
	}

	chartWidth := m.computeChartWidth()

	// Total plan duration
	totalDur := time.Duration(0)
	for _, s := range stages {
		totalDur += s.Duration
	}
	if totalDur == 0 {
		totalDur = 1
	}

	peakRPS := m.config.PeakRPS()
	if peakRPS < 1 {
		peakRPS = 1
	}

	// ── rpsAt: step function — plan shape matches config intent ──────────
	rpsAt := func(t time.Duration) float64 {
		acc := time.Duration(0)
		prev := 0.0
		for _, s := range stages {
			if s.Duration == 0 {
				prev = float64(s.TargetRPS)
				continue
			}
			end := acc + s.Duration
			if t < end {
				return float64(s.TargetRPS)
			}
			acc = end
			prev = float64(s.TargetRPS)
		}
		return prev
	}

	// Sample plan curve
	curve := make([]float64, chartWidth)
	for x := 0; x < chartWidth; x++ {
		t := time.Duration(float64(totalDur) * float64(x) / float64(chartWidth))
		curve[x] = rpsAt(t)
	}

	// Stage boundary columns
	boundaryCol := make(map[int]bool)
	{
		acc := time.Duration(0)
		for i, s := range stages {
			if s.Duration == 0 {
				continue
			}
			acc += s.Duration
			if i < len(stages)-1 {
				bx := timeToCol(acc, totalDur, chartWidth)
				if bx > 0 {
					boundaryCol[bx] = true
				}
			}
		}
	}

	// Cursor — derived from elapsed time, always correct after resize.
	// Stage durations from config are unscaled; wall-clock time runs faster
	// when time_scale > 1. Scale each stage duration before accumulating so
	// the cursor position is consistent with how fast the run actually moves.
	timeScale := m.config.ConfigSection.TimeScale
	if timeScale <= 0 {
		timeScale = 1.0
	}
	scaledTotalDur := time.Duration(float64(totalDur) / timeScale)
	if scaledTotalDur == 0 {
		scaledTotalDur = 1
	}
	totalElapsed := time.Duration(0)
	for i := 0; i < m.currentStage && i < len(stages); i++ {
		totalElapsed += time.Duration(float64(stages[i].Duration) / timeScale)
	}
	totalElapsed += m.stageElapsed
	cursorX := timeToCol(totalElapsed, scaledTotalDur, chartWidth)

	// ── Project time-slot history onto columns ────────────────────────────
	// totalSlots must be based on the wall-clock duration of the run, not the
	// unscaled plan duration. cfg.TotalDuration() applies TimeScale so that
	// e.g. time_scale:2 halves the wall-clock duration and the history slots
	// span the full chart width correctly.
	wallDur := m.config.TotalDuration()
	if wallDur == 0 {
		wallDur = 1
	}
	totalSlots := int(wallDur/historyInterval) + 1
	// historyByCol[x] = best (highest) actual RPS seen for that column, -1 = no data
	historyByCol := make([]float64, chartWidth)
	for i := range historyByCol {
		historyByCol[i] = -1
	}
	for slot, rps := range m.rpsHistory {
		col := slotToCol(slot, totalSlots, chartWidth)
		if col < chartWidth && rps > historyByCol[col] {
			historyByCol[col] = rps
		}
	}

	// Height helpers
	blocks := []rune{' ', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

	toHeight := func(rps float64) float64 {
		return (rps / float64(peakRPS)) * float64(chartHeight)
	}
	blockChar := func(h, rowBase float64) rune {
		if h >= rowBase+1.0 {
			return '█'
		}
		if h <= rowBase {
			return ' '
		}
		idx := int((h - rowBase) * float64(len(blocks)))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		return blocks[idx]
	}

	// Build grid
	type cell struct {
		ch rune
		st lipgloss.Style
	}
	grid := make([][]cell, chartHeight)
	for r := range grid {
		grid[r] = make([]cell, chartWidth)
	}

	for x := 0; x < chartWidth; x++ {
		targetH := toHeight(curve[x])
		isCursor := x == cursorX
		isPast := x < cursorX
		isBoundary := boundaryCol[x]

		// Compute marker row for past columns
		markerRow := -1
		markerOk := false
		if isPast && historyByCol[x] >= 0 {
			hh := toHeight(historyByCol[x])
			mr := chartHeight - 1 - int(hh)
			if mr < 0 {
				mr = 0
			}
			if mr >= chartHeight {
				mr = chartHeight - 1
			}
			markerRow = mr
			markerOk = targetH > 0 && hh >= targetH*0.9
		}

		for r := 0; r < chartHeight; r++ {
			rowBase := float64(chartHeight - 1 - r)
			var ch rune
			var st lipgloss.Style

			inBar := targetH > rowBase // this row is within the bar height

			if isCursor {
				if r == 0 {
					// Playhead indicator at top of cursor column
					ch = '▼'
					st = styles.Playhead
				} else if inBar {
					ch = blockChar(targetH, rowBase)
					st = styles.Playhead
				} else {
					ch = '│'
					st = styles.Playhead
				}
			} else if isBoundary {
				// Boundary line spans full column height
				ch = '▏'
				st = styles.Boundary
			} else if isPast {
				if inBar {
					ch = blockChar(targetH, rowBase)
					st = styles.PastBar
				} else {
					// Dimmed fill above the past bar — gives a "filled area" look
					ch = '░'
					st = styles.PastEmpty
				}
				// Overlay ▸ marker at the actual-RPS row
				if r == markerRow {
					ch = '▸'
					if markerOk {
						st = styles.SuccessBold
					} else {
						st = styles.ErrorBold
					}
				}
			} else {
				// Future
				if inBar {
					ch = blockChar(targetH, rowBase)
					st = styles.FutureBar
				} else if r == chartHeight-1 {
					// Floor line showing the plan exists
					ch = '▁'
					st = styles.FutureEmpty
				} else {
					ch = ' '
					st = styles.FutureEmpty
				}
			}

			grid[r][x] = cell{ch, st}
		}
	}

	// Assemble
	var sb strings.Builder

	// In journey mode, Target RPS is an iteration arrival rate while the
	// measured curve is the HTTP request rate — label units accordingly.
	targetUnit, actualUnit := "rps", "rps"
	if m.metrics.IsJourneyMode {
		targetUnit = "iter/s"
	}

	sb.WriteString(styles.SectionTitle.Render("STAGE PLAN"))
	sb.WriteString(fmt.Sprintf("  %s %s · %s %s · %s · %s on-target · %s off-target\n",
		styles.SectionTitle.Render("target"),
		styles.SectionTitle.Render(fmt.Sprintf("%4d %s", m.metrics.TargetRPS, targetUnit)),
		styles.Success.Render("actual"),
		styles.Success.Render(fmt.Sprintf("%4.0f %s", m.metrics.Throughput, actualUnit)),
		styles.Highlight.Render(fmt.Sprintf("%5d active actors", m.metrics.ActiveVPUs)),
		styles.SuccessBold.Render("▸"),
		styles.ErrorBold.Render("▸"),
	))

	// y-axis tick positions

	yLabelWidth := m.yAxisWidth() - 1
	for r := 0; r < chartHeight; r++ {
		var yLabel string
		var axisChar string
		switch r {
		case 0:
			yLabel = fmt.Sprintf("%*d", yLabelWidth, peakRPS)
			axisChar = "┤"
		case chartHeight / 2:
			yLabel = fmt.Sprintf("%*d", yLabelWidth, peakRPS/2)
			axisChar = "┤"
		case chartHeight - 1:
			yLabel = fmt.Sprintf("%*d", yLabelWidth, 0)
			axisChar = "┤"
		default:
			yLabel = strings.Repeat(" ", yLabelWidth)
			axisChar = "│"
		}
		sb.WriteString(styles.MetricLabel.Render(yLabel+" ") + styles.Boundary.Render(axisChar))
		for x := 0; x < chartWidth; x++ {
			c := grid[r][x]
			sb.WriteString(c.st.Render(string(c.ch)))
		}
		sb.WriteString("\n")
	}

	// X-axis with ┴ ticks at stage boundaries
	xAxis := make([]rune, chartWidth)
	for i := range xAxis {
		if boundaryCol[i] {
			xAxis[i] = '┴'
		} else {
			xAxis[i] = '─'
		}
	}
	sb.WriteString(styles.MetricLabel.Render(strings.Repeat(" ", m.yAxisWidth())) + styles.Boundary.Render("└"+string(xAxis)) + "\n")

	// Stage number labels
	labelRow := make([]rune, chartWidth)
	for i := range labelRow {
		labelRow[i] = ' '
	}
	{
		acc := time.Duration(0)
		for i, s := range stages {
			if s.Duration == 0 {
				continue
			}
			startX := timeToCol(acc, totalDur, chartWidth)
			acc += s.Duration
			endX := timeToCol(acc, totalDur, chartWidth)
			if endX > chartWidth {
				endX = chartWidth
			}
			w := endX - startX
			if w < 2 {
				continue
			}
			lbl := fmt.Sprintf("%d", i+1)
			pos := startX + (w-len(lbl))/2
			for k, c := range lbl {
				if p := pos + k; p >= 0 && p < chartWidth {
					labelRow[p] = c
				}
			}
		}
	}
	sb.WriteString(strings.Repeat(" ", m.yAxisWidth()+1))
	sb.WriteString(styles.MetricLabel.Render(string(labelRow)) + "\n")

	// Info bar
	stageIdx := m.currentStage
	if stageIdx >= len(stages) {
		stageIdx = len(stages) - 1
	}
	prevRPS := 0
	if stageIdx > 0 {
		prevRPS = stages[stageIdx-1].TargetRPS
	}
	stageLbl := fmt.Sprintf("[%d/%d] %s", stageIdx+1, len(stages), stages[stageIdx].Label(prevRPS))
	scaledStageDur := time.Duration(float64(stages[stageIdx].Duration) / timeScale)
	infoBar := fmt.Sprintf("%s  •  stage %s / %s  •  total %s / %s",
		stageLbl,
		formatDuration(m.stageElapsed), formatDuration(scaledStageDur),
		formatDuration(totalElapsed), formatDuration(scaledTotalDur),
	)
	sb.WriteString(styles.MetricLabel.Render(infoBar))

	return styles.PanelBase.Width(m.width - 4).Render(sb.String())
}

// ── formatDuration ────────────────────────────────────────────────────────────

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	mins := d / time.Minute
	secs := (d % time.Minute) / time.Second
	if mins > 0 {
		return fmt.Sprintf("%dm%ds", mins, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

// ── renderKeycap ─────────────────────────────────────────────────────────────

func renderKeycap(key string) string {
	return styles.Keycap.Render(key)
}

// ── renderMethodBadge / renderStatusBadge ────────────────────────────────────

func renderMethodBadge(method string) string {
	switch method {
	case "GET":
		return styles.BadgeGET.Render(method)
	case "POST":
		return styles.BadgePOST.Render(method)
	case "DELETE":
		return styles.BadgeDELETE.Render(method)
	default:
		return styles.BadgeMethod.Render(method)
	}
}

func renderStatusBadge(code int, errMsg string) string {
	if errMsg != "" {
		return styles.BadgeError.Render("ERR")
	}
	text := fmt.Sprintf("%d", code)
	switch {
	case code >= 200 && code < 300:
		return styles.Badge2xx.Render(text)
	case code >= 400 && code < 500:
		return styles.Badge4xx.Render(text)
	case code >= 500:
		return styles.Badge5xx.Render(text)
	default:
		return styles.BadgeMethod.Render(text)
	}
}

// padToWidth right-pads a rendered (potentially ANSI-escaped) string to the
// target visual width by appending plain spaces.
func padToWidth(s string, width int) string {
	if v := lipgloss.Width(s); v < width {
		return s + strings.Repeat(" ", width-v)
	}
	return s
}

// truncatePath truncates a URL path to maxLen runes and space-pads to maxLen.
func truncatePath(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s + strings.Repeat(" ", maxLen-len(runes))
	}
	return string(runes[:maxLen-1]) + "…"
}

// ── renderLogContent ──────────────────────────────────────────────────────────

// Column widths (visual chars):
//
//	timestamp(8) · method(8) · url(dynamic) · status(5) · latency(8)
const (
	logColTS      = 8 // "15:04:05"
	logColMethod  = 8 // widest badge: "DELETE" + 2 padding
	logColStatus  = 5 // "200" or "ERR" + 2 padding
	logColLatency = 8 // up to "99999ms"
	logColGaps    = 4 // four single-space separators
)

func (m model) renderLogContent() string {
	var logs []engine.CallLog
	if m.showFailures {
		logs = m.engine.GetRecentErrorLogs(100)
	} else {
		logs = m.engine.GetRecentLogs(100)
	}

	// PanelBase.Width(n) sets the inside-border dimension (content + padding).
	// Padding(0,1) consumes 2 of those chars, leaving n-2 for actual text.
	contentWidth := m.logView.Width - 2
	if contentWidth < 40 {
		contentWidth = 40
	}
	urlWidth := contentWidth - logColTS - logColMethod - logColStatus - logColLatency - logColGaps
	if urlWidth < 10 {
		urlWidth = 10
	}

	var lines []string
	for _, log := range logs {
		latencyStr := fmt.Sprintf("%dms", log.Duration.Milliseconds())
		if len(latencyStr) > logColLatency {
			latencyStr = latencyStr[:logColLatency]
		}

		line := fmt.Sprintf("%s %s %s %s %s",
			styles.Body.Render(log.Timestamp.Format("15:04:05")),
			padToWidth(renderMethodBadge(log.Method), logColMethod),
			styles.Body.Render(truncatePath(log.Url, urlWidth)),
			padToWidth(renderStatusBadge(log.StatusCode, log.Error), logColStatus),
			styles.Body.Render(fmt.Sprintf("%*s", logColLatency, latencyStr)),
		)
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return styles.Body.Render("Waiting for traffic (or no errors found)...")
	}
	return strings.Join(lines, "\n")
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.cancel()
			return m, tea.Quit
		case "f":
			m.showFailures = !m.showFailures
		case "up":
			if m.running {
				m.engine.ApplyBias(5)
				m.directorMsg = "▲  +5 RPS"
				m.directorMsgTime = time.Now()
			}
		case "down":
			if m.running {
				m.engine.ApplyBias(-5)
				m.directorMsg = "▼  -5 RPS"
				m.directorMsgTime = time.Now()
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		// Recompute layout from new dimensions — history is unaffected.
		l := m.computeLayout()
		m.logView.Width = l.logWidth
		m.logView.Height = l.logHeight
		m.logView.SetContent(m.renderLogContent())

	case tickMsg:
		wasRunning := m.running
		m.running = m.engine.IsRunning()
		m.metrics = m.engine.GetMetrics()

		// Engine finished all stages naturally — dispatch post-run work
		// (e.g. write snapshot) as a background tea.Cmd so the update
		// loop is never blocked and alt-screen rendering stays clean.
		if wasRunning && !m.running && m.onRunComplete != nil {
			fn := m.onRunComplete
			m.onRunComplete = nil
			cmds = append(cmds, func() tea.Msg {
				return snapFinalizedMsg{status: fn()}
			})
		}

		// Expire director feedback message after 3s
		if m.directorMsg != "" && time.Since(m.directorMsgTime) > 3*time.Second {
			m.directorMsg = ""
		}

		if m.running && !wasRunning {
			m.runStart = time.Now()
			m.stageStartTime = time.Now()
		}

		// Sync stage index from engine atomics
		if m.running {
			engineStage := m.metrics.CurrentStage
			if engineStage != m.currentStage {
				m.currentStage = engineStage
				m.stageStartTime = time.Now()
				m.stageElapsed = 0
			} else if !m.stageStartTime.IsZero() {
				m.stageElapsed = time.Since(m.stageStartTime)
			}
		}

		// Record RPS by time-slot — resize-safe
		if m.running && !m.runStart.IsZero() {
			slot := int(time.Since(m.runStart) / historyInterval)
			for slot >= len(m.rpsHistory) {
				m.rpsHistory = append(m.rpsHistory, 0)
			}
			if m.metrics.Throughput > m.rpsHistory[slot] {
				m.rpsHistory[slot] = m.metrics.Throughput
			}
		}

		if m.ready {
			bannerShown := m.metrics != nil && m.metrics.DroppedCount > 0
			if bannerShown != m.droppedBannerShown {
				m.droppedBannerShown = bannerShown
				l := m.computeLayout()
				m.logView.Width = l.logWidth
				m.logView.Height = l.logHeight
			}

			atBottom := m.logView.AtBottom()
			m.logView.SetContent(m.renderLogContent())
			if atBottom {
				m.logView.GotoBottom()
			}
		}
		cmds = append(cmds, m.tickCmd())
	case snapFinalizedMsg:
		if msg.status != "" {
			m.snapStatus = msg.status
		}

	}

	m.logView, cmd = m.logView.Update(msg)
	cmds = append(cmds, cmd)
	m.spinner, cmd = m.spinner.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m model) View() string {
	if !m.ready {
		return "\n  Loading..."
	}
	if m.width < minWidth {
		return fmt.Sprintf("\n  Terminal too narrow (%d cols). Please resize to at least %d cols.", m.width, minWidth)
	}

	header := m.renderHeader()
	timeline := m.renderTimeline()
	headerH := lipgloss.Height(header)
	timelineH := lipgloss.Height(timeline)

	// The header, timeline, director bar, log box border, and a minimum
	// 3-line log area are all fixed-height. If the terminal is shorter than
	// their combined height, the alt-screen silently scrolls the overflow
	// off the top — pushing the header (the first thing rendered) out of
	// view. Bail out with a resize prompt instead, mirroring the width check
	// above.
	bannerH := 0
	if m.metrics.DroppedCount > 0 {
		bannerH = 3
	}
	const directorBarH, logBorderH, minLogH = 1, 2, 3
	minHeight := headerH + timelineH + directorBarH + logBorderH + minLogH + bannerH
	if m.height < minHeight {
		return fmt.Sprintf("\n  Terminal too short (%d rows). Please resize to at least %d rows.", m.height, minHeight)
	}

	l := m.computeLayoutForHeights(headerH, timelineH)

	// ── director / hint bar ───────────────────────────────────────────────
	biasStr := ""
	if bias := m.metrics.Bias; bias != 0 {
		bs := styles.SuccessBold
		if bias < 0 {
			bs = styles.ErrorBold
		}
		biasStr = "  " + bs.Render(fmt.Sprintf("BIAS %+d RPS", bias))
	}
	feedbackStr := ""
	if m.directorMsg != "" {
		feedbackStr = "  " + styles.Highlight.Render(m.directorMsg)
	}
	logMode := "FAILURES ONLY"
	if !m.showFailures {
		logMode = "ALL LOGS"
	}
	directorBar := renderKeycap("↑") + styles.SuccessBold.Render(" +5 rps") + "  " +
		renderKeycap("↓") + styles.ErrorBold.Render(" -5 rps") + "  " +
		renderKeycap("f") + styles.MetricLabel.Render(fmt.Sprintf(" logs (%s)", logMode)) + "  " +
		renderKeycap("q") + styles.MetricLabel.Render(" quit") +
		biasStr + feedbackStr

	if m.snapStatus != "" {
		directorBar += styles.SuccessBold.Render("  " + m.snapStatus)
	} else if m.snapping {
		directorBar += styles.SuccessBold.Render(fmt.Sprintf("  📸 Snapping → %s", m.snapDir))
	}

	logBox := styles.PanelBase.Width(l.logWidth).Height(l.logHeight).Render(m.logView.View())

	var bottomBlocks []string

	if m.metrics.DroppedCount > 0 {
		warningBanner := lipgloss.NewStyle().
			Background(theme().Warning).
			Foreground(theme().OnAccent).
			Bold(true).
			Padding(0, 1).
			MarginTop(1).
			MarginBottom(1).
			Render(" [WARNING] Engine Throttling: Target Unresponsive ")
		bottomBlocks = append(bottomBlocks, warningBanner)
	}

	bottomBlocks = append(bottomBlocks, logBox)

	footerStr := lipgloss.JoinVertical(lipgloss.Left, bottomBlocks...)

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		timeline,
		directorBar,
		footerStr,
	)
}

// ── Start ─────────────────────────────────────────────────────────────────────

// Start launches the Bubble Tea TUI.
//   - snapping / snapDir  → drives the 📸 indicator
//   - onRunComplete       → called in a background goroutine once the engine
//     finishes all stages; must not write to stdout/stderr. The returned
//     string is shown in the director bar. nil is safe (no-op).
func Start(eng engine.Runner, cfg *config.Config, specs []httpreader.RequestSpec, snapping bool, snapDir string, onRunComplete func() string, tickInterval time.Duration) error {
	m := initialModel(eng, cfg, specs)
	m.snapping = snapping
	m.snapDir = snapDir
	m.onRunComplete = onRunComplete
	m.tickInterval = tickInterval
	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := p.Run()

	// Block until the engine goroutine has fully exited.
	// This guarantees that when Start returns — whether because the run
	// completed naturally or the user pressed [q] — no engine worker is
	// still calling recorder.Record(), making it safe for the caller to
	// invoke finalizeSnapResult immediately afterwards.
	<-m.engineDone

	return err
}
