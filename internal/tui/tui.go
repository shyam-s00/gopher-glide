package tui

import (
	"context"
	"fmt"
	"gopher-glide/internal/config"
	"gopher-glide/internal/engine"
	"gopher-glide/internal/httpreader"
	"strings"
	"time"

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
)

// ── model ─────────────────────────────────────────────────────────────────────

type model struct {
	engine  *engine.Engine
	config  *config.Config
	specs   []httpreader.RequestSpec
	ctx     context.Context
	cancel  context.CancelFunc
	logView viewport.Model

	width  int
	height int
	ready  bool

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
}

type tickMsg time.Time

// ── init ──────────────────────────────────────────────────────────────────────

func initialModel(eng *engine.Engine, cfg *config.Config, specs []httpreader.RequestSpec) model {
	ctx, cancel := context.WithCancel(context.Background())
	vp := viewport.New(0, 0)
	return model{
		engine:       eng,
		config:       cfg,
		specs:        specs,
		ctx:          ctx,
		cancel:       cancel,
		logView:      vp,
		metrics:      &engine.MetricsSnapshot{},
		showFailures: true,
		rpsHistory:   make([]float64, 0, 256),
	}
}

func (m model) Init() tea.Cmd {
	go func() {
		_ = m.engine.RunStages(m.ctx, m.config, m.specs)
	}()
	return tea.Batch(tickCmd(), tea.EnterAltScreen)
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// ── layout ────────────────────────────────────────────────────────────────────

type layout struct {
	chartWidth int
	logHeight  int
	logWidth   int
}

func (m model) computeLayout() layout {
	w := m.width
	if w < minWidth {
		w = minWidth
	}
	cw := w - yAxisWidth - 1 - 6
	if cw < 20 {
		cw = 20
	}
	// header: title(3) + stat boxes(18) + margin(1) = 22
	// timeline: title(1) + chart(10) + x-axis(1) + labels(1) + info(2) + border(2) + margin(1) = 18
	// debug header: 1
	// log border: 2
	used := 22 + 18 + 1 + 2
	logH := m.height - used
	if logH < 3 {
		logH = 3
	}
	return layout{
		chartWidth: cw,
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
		Width(28)

	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Bold(true)
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87")).Bold(true)

	statusStr := "STOPPED"
	statusColor := lipgloss.Color("#FF5F87")
	if m.running {
		statusStr = "RUNNING"
		statusColor = lipgloss.Color("#04B575")
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

	configuration := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("CONFIGURATION"),
		labelStyle.Render("Status:"),
		lipgloss.NewStyle().Foreground(statusColor).Bold(true).Render(statusStr),
		labelStyle.Render("Uptime:"),
		valueStyle.Render(fmt.Sprintf("%.2fs", elapsed)),
		labelStyle.Render("Http File:"),
		valueStyle.Render(m.config.ConfigSection.HTTPFile),
		labelStyle.Render("Active VPU:"),
		valueStyle.Render(fmt.Sprintf("%d", m.metrics.ActiveVPUs)),
		labelStyle.Render("Target RPS:"),
		valueStyle.Render(fmt.Sprintf("%d", m.metrics.TargetRPS)),
		labelStyle.Render("Stage:"),
		valueStyle.Render(stageLabel),
		"",
	)

	jitterVal := m.config.ConfigSection.Jitter
	jitterStr := "off"
	if jitterVal > 0 {
		jitterStr = fmt.Sprintf("±%.0f%%", jitterVal*100)
	}

	throughput := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("THROUGHPUT"),
		labelStyle.Render("RPS:"),
		valueStyle.Render(fmt.Sprintf("%.2f", m.metrics.Throughput)),
		labelStyle.Render("Total Requests:"),
		valueStyle.Render(fmt.Sprintf("%d", m.metrics.TotalRequests)),
		labelStyle.Render("Success:"),
		successStyle.Render(fmt.Sprintf("%d", m.metrics.SuccessCount)),
		labelStyle.Render("Failed:"),
		errorStyle.Render(fmt.Sprintf("%d", m.metrics.FailureCount)),
		labelStyle.Render("ErrorRate:"),
		valueStyle.Render(fmt.Sprintf("%.2f%%", m.metrics.ErrorRate*100)),
		labelStyle.Render("Jitter:"),
		valueStyle.Render(jitterStr),
		"",
	)

	latency := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("LATENCY"),
		labelStyle.Render("Min:"),
		valueStyle.Render(fmt.Sprintf("%.2fms", m.metrics.MinLatency)),
		labelStyle.Render("Max:"),
		valueStyle.Render(fmt.Sprintf("%.2fms", m.metrics.MaxLatency)),
		labelStyle.Render("P50:"),
		valueStyle.Render(fmt.Sprintf("%.2fms", m.metrics.P50Latency)),
		labelStyle.Render("P95:"),
		valueStyle.Render(fmt.Sprintf("%.2fms", m.metrics.P95Latency)),
		labelStyle.Render("P99:"),
		valueStyle.Render(fmt.Sprintf("%.2fms", m.metrics.P99Latency)),
		"", "", "",
	)

	return lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Gopher Glide (GG)"),
		lipgloss.JoinHorizontal(lipgloss.Top,
			boxStyle.Render(configuration),
			boxStyle.Render(throughput),
			boxStyle.Render(latency),
		),
	)
}

// ── renderTimeline ────────────────────────────────────────────────────────────

func (m model) renderTimeline() string {
	stages := m.config.Stages
	if len(stages) == 0 {
		return ""
	}

	l := m.computeLayout()
	chartWidth := l.chartWidth

	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	pastBarStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4"))
	pastEmptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#2A1F5A")) // dimmed fill above past bar
	futureBarStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#3A3A3A")) // dark filled future bar
	futureEmptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#222222"))
	boundaryStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#444444"))
	cursorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD700")).Bold(true)
	actualStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575"))
	targetLiveStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#CCBBFF")).Bold(true)
	markerOkStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Bold(true)
	markerMissStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87")).Bold(true)

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
					st = cursorStyle
				} else if inBar {
					ch = blockChar(targetH, rowBase)
					st = cursorStyle
				} else {
					ch = '│'
					st = cursorStyle
				}
			} else if isBoundary {
				// Boundary line spans full column height
				ch = '▏'
				st = boundaryStyle
			} else if isPast {
				if inBar {
					ch = blockChar(targetH, rowBase)
					st = pastBarStyle
				} else {
					// Dimmed fill above the past bar — gives a "filled area" look
					ch = '░'
					st = pastEmptyStyle
				}
				// Overlay ▸ marker at the actual-RPS row
				if r == markerRow {
					ch = '▸'
					if markerOk {
						st = markerOkStyle
					} else {
						st = markerMissStyle
					}
				}
			} else {
				// Future
				if inBar {
					ch = blockChar(targetH, rowBase)
					st = futureBarStyle
				} else if r == chartHeight-1 {
					// Floor line showing the plan exists
					ch = '▁'
					st = futureEmptyStyle
				} else {
					ch = ' '
					st = futureEmptyStyle
				}
			}

			grid[r][x] = cell{ch, st}
		}
	}

	// Assemble
	var sb strings.Builder

	sb.WriteString(sectionStyle.Render("STAGE PLAN"))
	sb.WriteString(fmt.Sprintf("  %s %s · %s %s · %s on-target · %s off-target\n",
		targetLiveStyle.Render("target"),
		targetLiveStyle.Render(fmt.Sprintf("%d rps", m.metrics.TargetRPS)),
		actualStyle.Render("actual"),
		actualStyle.Render(fmt.Sprintf("%.0f rps", m.metrics.Throughput)),
		markerOkStyle.Render("▸"),
		markerMissStyle.Render("▸"),
	))

	// y-axis tick positions
	yTickRows := map[int]bool{0: true, chartHeight / 2: true, chartHeight - 1: true}

	for r := 0; r < chartHeight; r++ {
		var yLabel string
		var axisChar string
		switch r {
		case 0:
			yLabel = fmt.Sprintf("%4d", peakRPS)
			axisChar = "┤"
		case chartHeight / 2:
			yLabel = fmt.Sprintf("%4d", peakRPS/2)
			axisChar = "┤"
		case chartHeight - 1:
			yLabel = "   0"
			axisChar = "┤"
		default:
			yLabel = "    "
			axisChar = "│"
		}
		_ = yTickRows
		sb.WriteString(labelStyle.Render(yLabel+" ") + axisChar)
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
	sb.WriteString(labelStyle.Render(strings.Repeat(" ", yAxisWidth)) + "└" + string(xAxis) + "\n")

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
	sb.WriteString(strings.Repeat(" ", yAxisWidth+1))
	sb.WriteString(labelStyle.Render(string(labelRow)) + "\n")

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
	sb.WriteString("\n")
	sb.WriteString(labelStyle.Render(infoBar))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#63")).
		Padding(0, 1).
		MarginTop(1).
		Width(m.width - 4).
		Render(sb.String())
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

// ── renderLogContent ──────────────────────────────────────────────────────────

func (m model) renderLogContent() string {
	var logs []engine.CallLog
	if m.showFailures {
		logs = m.engine.GetRecentErrorLogs(100)
	} else {
		logs = m.engine.GetRecentLogs(100)
	}

	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87"))
	normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	var lines []string
	for _, log := range logs {
		var statusStr string
		var ss lipgloss.Style
		if log.Error != "" {
			statusStr = fmt.Sprintf("[ERROR] %s", log.Error)
			ss = errorStyle
		} else if log.StatusCode >= 200 && log.StatusCode < 300 {
			statusStr = fmt.Sprintf("[%d]", log.StatusCode)
			ss = successStyle
		} else {
			statusStr = fmt.Sprintf("[%d]", log.StatusCode)
			ss = errorStyle
		}
		lines = append(lines, fmt.Sprintf("%s %s %s %s %s",
			normalStyle.Render(log.Timestamp.Format("15:04:05")),
			normalStyle.Render(log.Method),
			normalStyle.Render(log.Url),
			ss.Render(statusStr),
			normalStyle.Render(fmt.Sprintf("%dms", log.Duration.Milliseconds())),
		))
	}
	if len(lines) == 0 {
		return normalStyle.Render("Waiting for traffic (or no errors found)...")
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
			atBottom := m.logView.AtBottom()
			m.logView.SetContent(m.renderLogContent())
			if atBottom {
				m.logView.GotoBottom()
			}
		}
		cmds = append(cmds, tickCmd())
	}

	m.logView, cmd = m.logView.Update(msg)
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

	l := m.computeLayout()

	// ── director / hint bar ───────────────────────────────────────────────
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	biasUpStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#04B575"))
	biasDownStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF5F87"))
	msgStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFD700"))

	biasStr := ""
	if bias := m.metrics.Bias; bias != 0 {
		bs := biasUpStyle
		if bias < 0 {
			bs = biasDownStyle
		}
		biasStr = "  " + bs.Render(fmt.Sprintf("BIAS %+d RPS", bias))
	}
	feedbackStr := ""
	if m.directorMsg != "" {
		feedbackStr = "  " + msgStyle.Render(m.directorMsg)
	}
	logMode := "FAILURES ONLY"
	if !m.showFailures {
		logMode = "ALL LOGS"
	}
	directorBar := biasUpStyle.Render("[↑]") + hintStyle.Render(" +5 rps  ") +
		biasDownStyle.Render("[↓]") + hintStyle.Render(" -5 rps  ") +
		hintStyle.Render(fmt.Sprintf("[f] logs (%s)  [q] quit", logMode)) +
		biasStr + feedbackStr

	logBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#63")).
		Padding(0, 1).
		MarginTop(1).
		Width(l.logWidth).
		Height(l.logHeight).
		Render(m.logView.View())

	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderHeader(),
		m.renderTimeline(),
		directorBar,
		logBox,
	)
}

// ── Start ─────────────────────────────────────────────────────────────────────

func Start(eng *engine.Engine, cfg *config.Config, specs []httpreader.RequestSpec) error {
	p := tea.NewProgram(
		initialModel(eng, cfg, specs),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := p.Run()
	return err
}
