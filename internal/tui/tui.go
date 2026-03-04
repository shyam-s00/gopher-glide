package tui

import (
	"context"
	"fmt"
	"gopher-glide/internal/config"
	"gopher-glide/internal/engine"
	"gopher-glide/internal/httpreader"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type model struct {
	logView      viewport.Model
	engine       *engine.Engine
	config       *config.Config
	metrics      *engine.MetricsSnapshot
	specs        []httpreader.RequestSpec
	ctx          context.Context
	cancel       context.CancelFunc
	ready        bool
	running      bool
	width        int
	height       int
	showFailures bool
	// stage tracking
	currentStage   int           // index into config.Stages
	stageStartTime time.Time     // when the current stage began
	stageElapsed   time.Duration // how long we have been in the current stage
	planPaused     bool          // Director Mode: stage clock frozen
}

type tickMsg time.Time

func initialModel(eng *engine.Engine, cfg *config.Config, specs []httpreader.RequestSpec) model {
	ctx, cancel := context.WithCancel(context.Background())

	vp := viewport.New(0, 0)
	vp.YPosition = 0

	return model{
		engine:         eng,
		config:         cfg,
		specs:          specs,
		metrics:        &engine.MetricsSnapshot{},
		ctx:            ctx,
		cancel:         cancel,
		running:        false,
		showFailures:   true,
		logView:        vp,
		currentStage:   0,
		stageStartTime: time.Time{}, // set on the first tick once the engine starts
		planPaused:     false,
	}
}

func (m model) Init() tea.Cmd {
	go func() {
		stage := m.config.Stages[0]
		_ = m.engine.Run(m.ctx, stage.TargetRPS, stage.Duration, m.specs)
	}()

	return tea.Batch(tickCmd(), tea.EnterAltScreen)
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// ── renderHeader ─────────────────────────────────────────────────────────────

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

	sectionStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7D56F4"))

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))

	valueStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Bold(true)

	successStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#04B575")).
		Bold(true)

	errorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FF5f87")).
		Bold(true)

	statusStr := "STOPPED"
	statusColor := lipgloss.Color("#FF5F87")
	if m.running {
		statusStr = "RUNNING"
		statusColor = lipgloss.Color("#04B575")
	}

	// Infer stage label for the current stage
	stages := m.config.Stages
	prevRPS := 0
	if m.currentStage > 0 {
		prevRPS = stages[m.currentStage-1].TargetRPS
	}
	stageLabel := "–"
	if len(stages) > 0 {
		stageLabel = fmt.Sprintf("[%d/%d] %s",
			m.currentStage+1,
			len(stages),
			stages[m.currentStage].Label(prevRPS),
		)
	}

	configuration := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("CONFIGURATION"),
		labelStyle.Render("Status:"), lipgloss.NewStyle().Foreground(statusColor).Bold(true).Render(statusStr),
		labelStyle.Render("Uptime:"), valueStyle.Render(fmt.Sprintf("%.2fs", elapsed)),
		labelStyle.Render("Http File:"), valueStyle.Render(m.config.ConfigSection.HTTPFile),
		labelStyle.Render("Active VPU:"), valueStyle.Render(fmt.Sprintf("%d", m.metrics.ActiveVPUs)),
		labelStyle.Render("Target RPS:"), valueStyle.Render(fmt.Sprintf("%d", m.metrics.TargetRPS)),
		labelStyle.Render("Stage:"), valueStyle.Render(stageLabel),
		"")

	throughput := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("THROUGHPUT"),
		labelStyle.Render("RPS:"), valueStyle.Render(fmt.Sprintf("%.2f", m.metrics.Throughput)),
		labelStyle.Render("Total Requests:"), valueStyle.Render(fmt.Sprintf("%d", m.metrics.TotalRequests)),
		labelStyle.Render("Success:"), successStyle.Render(fmt.Sprintf("%d", m.metrics.SuccessCount)),
		labelStyle.Render("Failed:"), errorStyle.Render(fmt.Sprintf("%d", m.metrics.FailureCount)),
		labelStyle.Render("ErrorRate:"), valueStyle.Render(fmt.Sprintf("%.2f%%", m.metrics.ErrorRate*100)),
		"", "", "")

	latency := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("LATENCY"),
		labelStyle.Render("Min:"), valueStyle.Render(fmt.Sprintf("%.2fms", m.metrics.MinLatency)),
		labelStyle.Render("Max:"), valueStyle.Render(fmt.Sprintf("%.2fms", m.metrics.MaxLatency)),
		labelStyle.Render("P50:"), valueStyle.Render(fmt.Sprintf("%.2fms", m.metrics.P50Latency)),
		labelStyle.Render("P95:"), valueStyle.Render(fmt.Sprintf("%.2fms", m.metrics.P95Latency)),
		labelStyle.Render("P99:"), valueStyle.Render(fmt.Sprintf("%.2fms", m.metrics.P99Latency)),
		"", "", "")

	header := titleStyle.Render("Gopher Glide (GG)")
	stats := lipgloss.JoinHorizontal(lipgloss.Top,
		boxStyle.Render(configuration),
		boxStyle.Render(throughput),
		boxStyle.Render(latency),
	)

	return lipgloss.JoinVertical(lipgloss.Left, header, stats)
}

// ── renderTimeline ────────────────────────────────────────────────────────────

// renderTimeline draws an ASCII chart of the full stage plan and a cursor
// showing the current position in the plan.
//
// Example output (width=60):
//
//	STAGE PLAN ─────────────────────────────────────────────
//	 200 │         ╭────────╮
//	 100 │    ╱────╯        ╰──────╮
//	   0 │───╯                     ╰───
//	     └───────────────────────────────
//	       ▲
//	       NOW
//	[2/5] Sustain  •  stage 18s / 1m0s  •  total 48s / 2m10s
func (m model) renderTimeline() string {
	stages := m.config.Stages
	if len(stages) == 0 {
		return ""
	}

	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	cursorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD700")).Bold(true)
	pauseStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87")).Bold(true)

	// ── chart geometry ───────────────────────────────────────────
	chartWidth := m.width - 12 // leave room for y-axis labels + borders
	if chartWidth < 20 {
		chartWidth = 20
	}
	chartHeight := 5 // number of rows for the RPS curve

	peakRPS := m.config.PeakRPS()
	if peakRPS == 0 {
		peakRPS = 1 // guard divide-by-zero
	}

	// Total plan duration (unscaled — we show real wall-clock shape)
	totalDur := time.Duration(0)
	for _, s := range stages {
		totalDur += s.Duration
	}
	if totalDur == 0 {
		totalDur = 1
	}

	// ── build RPS curve: sample chartWidth points ─────────────────
	// For each x column, compute what the RPS would be at that time
	// using linear interpolation (lerp) between stage boundaries.
	rpsAt := func(t time.Duration) float64 {
		elapsed := time.Duration(0)
		prevRPS := 0.0
		for _, s := range stages {
			stageEnd := elapsed + s.Duration
			if s.Duration == 0 {
				// instant spike — skip to target immediately
				prevRPS = float64(s.TargetRPS)
				continue
			}
			if t <= stageEnd {
				// t falls inside this stage — lerp
				progress := float64(t-elapsed) / float64(s.Duration)
				return prevRPS + (float64(s.TargetRPS)-prevRPS)*progress
			}
			elapsed = stageEnd
			prevRPS = float64(s.TargetRPS)
		}
		return prevRPS
	}

	curve := make([]float64, chartWidth)
	for x := 0; x < chartWidth; x++ {
		t := time.Duration(float64(totalDur) * float64(x) / float64(chartWidth-1))
		curve[x] = rpsAt(t)
	}

	// ── render the grid ───────────────────────────────────────────
	// grid[row][col] — row 0 = top (peakRPS), row chartHeight-1 = bottom (0)
	grid := make([][]rune, chartHeight)
	for r := range grid {
		grid[r] = make([]rune, chartWidth)
		for c := range grid[r] {
			grid[r][c] = ' '
		}
	}

	// Plot curve points; connect consecutive columns with box-drawing chars.
	colToRow := func(rps float64) int {
		row := int(math.Round(float64(chartHeight-1) * (1.0 - rps/float64(peakRPS))))
		if row < 0 {
			row = 0
		}
		if row >= chartHeight {
			row = chartHeight - 1
		}
		return row
	}

	for x := 0; x < chartWidth; x++ {
		r := colToRow(curve[x])
		if x == 0 {
			grid[r][x] = '─'
			continue
		}
		prev := colToRow(curve[x-1])
		switch {
		case prev == r:
			grid[r][x] = '─'
		case prev > r: // going up
			grid[r][x] = '╭'
			for fill := r + 1; fill < prev; fill++ {
				grid[fill][x] = '│'
			}
			grid[prev][x] = '╯'
		default: // going down
			grid[prev][x] = '╮'
			for fill := prev + 1; fill < r; fill++ {
				grid[fill][x] = '│'
			}
			grid[r][x] = '╰'
		}
	}

	// ── cursor column ─────────────────────────────────────────────
	// totalElapsed = sum of completed stages + stageElapsed in current stage
	totalElapsed := time.Duration(0)
	for i := 0; i < m.currentStage && i < len(stages); i++ {
		totalElapsed += stages[i].Duration
	}
	totalElapsed += m.stageElapsed

	cursorX := 0
	if totalDur > 0 {
		cursorX = int(float64(chartWidth-1) * float64(totalElapsed) / float64(totalDur))
	}
	if cursorX >= chartWidth {
		cursorX = chartWidth - 1
	}

	// ── assemble chart rows ───────────────────────────────────────
	yAxisWidth := 5
	var sb strings.Builder

	// Title row
	sb.WriteString(sectionStyle.Render("STAGE PLAN") + "\n")

	for r := 0; r < chartHeight; r++ {
		// y-axis label on first, middle and last row
		yLabel := "     "
		switch r {
		case 0:
			yLabel = fmt.Sprintf("%4d ", peakRPS)
		case chartHeight / 2:
			yLabel = fmt.Sprintf("%4d ", peakRPS/2)
		case chartHeight - 1:
			yLabel = fmt.Sprintf("%4d ", 0)
		}
		sb.WriteString(labelStyle.Render(yLabel) + "│")

		rowStr := string(grid[r])
		sb.WriteString(valueStyle.Render(rowStr))
		sb.WriteString("\n")
	}

	// x-axis baseline
	sb.WriteString(strings.Repeat(" ", yAxisWidth) + "└" + strings.Repeat("─", chartWidth) + "\n")

	// cursor arrow row
	arrowRow := make([]rune, chartWidth)
	for i := range arrowRow {
		arrowRow[i] = ' '
	}
	arrowRow[cursorX] = '▲'
	arrowStr := string(arrowRow)
	sb.WriteString(strings.Repeat(" ", yAxisWidth+1)) // align with chart area
	if m.planPaused {
		sb.WriteString(pauseStyle.Render(arrowStr) + "\n")
	} else {
		sb.WriteString(cursorStyle.Render(arrowStr) + "\n")
	}

	// "NOW" label below cursor
	nowRow := make([]rune, chartWidth)
	for i := range nowRow {
		nowRow[i] = ' '
	}
	nowLabel := []rune("NOW")
	// centre the label on cursorX
	start := cursorX - len(nowLabel)/2
	for i, ch := range nowLabel {
		pos := start + i
		if pos >= 0 && pos < chartWidth {
			nowRow[pos] = ch
		}
	}
	sb.WriteString(strings.Repeat(" ", yAxisWidth+1))
	if m.planPaused {
		sb.WriteString(pauseStyle.Render(string(nowRow)) + "\n")
	} else {
		sb.WriteString(cursorStyle.Render(string(nowRow)) + "\n")
	}

	// ── info bar ──────────────────────────────────────────────────
	prevRPS := 0
	if m.currentStage > 0 {
		prevRPS = stages[m.currentStage-1].TargetRPS
	}
	stageLabelStr := fmt.Sprintf("[%d/%d] %s",
		m.currentStage+1,
		len(stages),
		stages[m.currentStage].Label(prevRPS),
	)

	stageDur := stages[m.currentStage].Duration
	stageRemaining := stageDur - m.stageElapsed
	if stageRemaining < 0 {
		stageRemaining = 0
	}

	totalRemaining := totalDur - totalElapsed
	if totalRemaining < 0 {
		totalRemaining = 0
	}

	pausedStr := ""
	if m.planPaused {
		pausedStr = pauseStyle.Render("  ⏸ PAUSED")
	}

	infoBar := fmt.Sprintf("%s  •  stage %s / %s  •  total %s / %s%s",
		stageLabelStr,
		formatDuration(m.stageElapsed),
		formatDuration(stageDur),
		formatDuration(totalElapsed),
		formatDuration(totalDur),
		pausedStr,
	)
	sb.WriteString(labelStyle.Render(infoBar))

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#63")).
		Padding(0, 1).
		MarginTop(1).
		Width(m.width - 4)

	return boxStyle.Render(sb.String())
}

// formatDuration formats a duration as a concise human string (e.g. "1m30s", "45s").
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	m := d / time.Minute
	s := (d % time.Minute) / time.Second
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
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
		timestamp := log.Timestamp.Format("15:04:05")
		duration := fmt.Sprintf("%dms", log.Duration.Milliseconds())

		var statusStr string
		var statusStyle lipgloss.Style
		if log.Error != "" {
			statusStr = fmt.Sprintf("[ERROR] %s", log.Error)
			statusStyle = errorStyle
		} else if log.StatusCode >= 200 && log.StatusCode < 300 {
			statusStr = fmt.Sprintf("[%d]", log.StatusCode)
			statusStyle = successStyle
		} else {
			statusStr = fmt.Sprintf("[%d]", log.StatusCode)
			statusStyle = errorStyle
		}

		line := fmt.Sprintf("%s %s %s %s %s",
			normalStyle.Render(timestamp),
			normalStyle.Render(log.Method),
			normalStyle.Render(log.Url),
			statusStyle.Render(statusStr),
			normalStyle.Render(duration))
		lines = append(lines, line)
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
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true

		// title(3) + stats(12) + timeline(~10) + debug header(2) + margins
		headerHeight := 33

		logWidth := msg.Width - 4
		if logWidth < 1 {
			logWidth = 1
		}
		logHeight := msg.Height - headerHeight - 3
		if logHeight < 1 {
			logHeight = 1
		}

		m.logView.Width = logWidth
		m.logView.Height = logHeight
		m.logView.SetContent(m.renderLogContent())

	case tickMsg:
		wasRunning := m.running
		m.running = m.engine.IsRunning()
		m.metrics = m.engine.GetMetrics()

		// Start stage clock on first tick the engine begins running
		if m.running && !wasRunning {
			m.stageStartTime = time.Now()
		}

		// Advance stage elapsed time unless paused
		if m.running && !m.planPaused && !m.stageStartTime.IsZero() {
			m.stageElapsed = time.Since(m.stageStartTime)

			// Advance stage index when elapsed exceeds current stage duration
			// (placeholder — engine will drive this in Phase 2)
			stages := m.config.Stages
			if m.currentStage < len(stages)-1 {
				stageDur := stages[m.currentStage].Duration
				if stageDur > 0 && m.stageElapsed >= stageDur {
					m.currentStage++
					m.stageStartTime = time.Now()
					m.stageElapsed = 0
				}
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
		return "Loading..."
	}

	header := m.renderHeader()
	timeline := m.renderTimeline()

	mode := "FAILURES ONLY"
	if !m.showFailures {
		mode = "ALL LOGS"
	}

	debugHeader := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7D56F4")).
		Render(fmt.Sprintf("[DEBUG] Press 'f' to toggle log mode (Current: %s)", mode))

	logBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#63")).
		Padding(0, 1).
		MarginTop(1).
		Width(m.logView.Width).
		Height(m.logView.Height).
		Render(m.logView.View())

	return lipgloss.JoinVertical(lipgloss.Left, header, timeline, debugHeader, logBox)
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
