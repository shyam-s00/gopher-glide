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
		_ = m.engine.RunStages(m.ctx, m.config, m.specs)
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

func (m model) renderTimeline() string {
	stages := m.config.Stages
	if len(stages) == 0 {
		return ""
	}

	// ── styles ────────────────────────────────────────────────────
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	pastStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4"))
	futureStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#555555"))
	cursorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD700")).Bold(true)
	actualStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575"))
	gapStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#333333"))
	overshootStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87"))
	targetLiveStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#CCBBFF")).Bold(true)

	// ── geometry ──────────────────────────────────────────────────
	// yAxisWidth(5) + "│"(1) + chartWidth + border/padding(~6)
	const yAxisWidth = 5
	const chartHeight = 10
	chartWidth := m.width - yAxisWidth - 1 - 6
	if chartWidth < 20 {
		chartWidth = 20
	}

	peakRPS := m.config.PeakRPS()
	if peakRPS < 1 {
		peakRPS = 1
	}

	// ── total duration (skip zero-duration spike stages in time budget) ──
	totalDur := time.Duration(0)
	for _, s := range stages {
		totalDur += s.Duration
	}
	if totalDur == 0 {
		totalDur = 1
	}

	// ── rpsAt: step function matching what the engine actually does ───────
	// Each stage holds its target_rps for its full duration.
	// Zero-duration stages are instant jumps (spike) with no time width.
	rpsAt := func(t time.Duration) float64 {
		acc := time.Duration(0)
		for _, s := range stages {
			if s.Duration == 0 {
				continue // spike: no time width, skip
			}
			end := acc + s.Duration
			if t < end {
				return float64(s.TargetRPS)
			}
			acc = end
		}
		// past the end — return last stage's target
		for i := len(stages) - 1; i >= 0; i-- {
			if stages[i].Duration > 0 {
				return float64(stages[i].TargetRPS)
			}
		}
		return 0
	}

	// ── sample the plan curve into chartWidth columns ─────────────────────
	curve := make([]float64, chartWidth)
	for x := 0; x < chartWidth; x++ {
		t := time.Duration(float64(totalDur) * float64(x) / float64(chartWidth))
		curve[x] = rpsAt(t)
	}

	// ── stage boundary columns ────────────────────────────────────────────
	boundaryCol := make(map[int]bool)
	{
		acc := time.Duration(0)
		for i, s := range stages {
			if s.Duration == 0 {
				continue
			}
			acc += s.Duration
			if i < len(stages)-1 {
				bx := int(float64(chartWidth) * float64(acc) / float64(totalDur))
				if bx > 0 && bx < chartWidth {
					boundaryCol[bx] = true
				}
			}
		}
	}

	// ── cursor position ───────────────────────────────────────────────────
	totalElapsed := time.Duration(0)
	for i := 0; i < m.currentStage && i < len(stages); i++ {
		totalElapsed += stages[i].Duration
	}
	totalElapsed += m.stageElapsed

	cursorX := int(float64(chartWidth) * float64(totalElapsed) / float64(totalDur))
	if cursorX < 0 {
		cursorX = 0
	}
	if cursorX >= chartWidth {
		cursorX = chartWidth - 1
	}

	// ── height helpers ────────────────────────────────────────────────────
	blocks := []rune{' ', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

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

	toHeight := func(rps float64) float64 {
		return (rps / float64(peakRPS)) * float64(chartHeight)
	}

	// ── build rune + style grids ──────────────────────────────────────────
	grid := make([][]rune, chartHeight)
	sg := make([][]lipgloss.Style, chartHeight)
	for r := range grid {
		grid[r] = make([]rune, chartWidth)
		sg[r] = make([]lipgloss.Style, chartWidth)
	}

	actualH := toHeight(m.metrics.Throughput)

	for x := 0; x < chartWidth; x++ {
		targetH := toHeight(curve[x])
		isCursor := x == cursorX

		for r := 0; r < chartHeight; r++ {
			rowBase := float64(chartHeight - 1 - r)
			var ch rune
			var st lipgloss.Style

			if isCursor {
				// ── cursor column: show actual (green) vs target (gap / overshoot) ──
				actualChar := blockChar(actualH, rowBase)
				targetChar := blockChar(targetH, rowBase)

				switch {
				case actualH >= rowBase+1.0:
					// fully filled by actual
					ch = '█'
					if targetH < rowBase+1.0 {
						st = overshootStyle // actual above target
					} else {
						st = actualStyle
					}
				case actualH > rowBase:
					// fractional actual block
					ch = actualChar
					st = actualStyle
					_ = targetChar
				case targetH >= rowBase+1.0:
					// above actual but target fills this row — show gap
					ch = '░'
					st = gapStyle
				case targetH > rowBase:
					// fractional gap
					ch = targetChar
					st = gapStyle
				default:
					// above both — draw cursor line
					ch = '┃'
					st = cursorStyle
				}
			} else {
				ch = blockChar(targetH, rowBase)
				if ch == ' ' && boundaryCol[x] {
					ch = '╎'
				}
				if x < cursorX {
					st = pastStyle
				} else {
					st = futureStyle
				}
			}

			grid[r][x] = ch
			sg[r][x] = st
		}
	}

	// ── assemble ──────────────────────────────────────────────────────────
	var sb strings.Builder

	// title row with live Target / Actual
	sb.WriteString(sectionStyle.Render("STAGE PLAN"))
	sb.WriteString(fmt.Sprintf("   TARGET: %s   ACTUAL: %s\n",
		targetLiveStyle.Render(fmt.Sprintf("%d rps", int(m.metrics.TargetRPS))),
		actualStyle.Render(fmt.Sprintf("%.0f rps", m.metrics.Throughput)),
	))

	// chart rows
	for r := 0; r < chartHeight; r++ {
		yLabel := "     "
		switch r {
		case 0:
			yLabel = fmt.Sprintf("%4d ", peakRPS)
		case chartHeight / 2:
			yLabel = fmt.Sprintf("%4d ", peakRPS/2)
		case chartHeight - 1:
			yLabel = "   0 "
		}
		sb.WriteString(labelStyle.Render(yLabel) + "│")
		for x := 0; x < chartWidth; x++ {
			sb.WriteString(sg[r][x].Render(string(grid[r][x])))
		}
		sb.WriteString("\n")
	}

	// x-axis
	sb.WriteString(strings.Repeat(" ", yAxisWidth) + "└" + strings.Repeat("─", chartWidth) + "\n")

	// stage number labels centred in each stage's column span
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
			startX := int(float64(chartWidth) * float64(acc) / float64(totalDur))
			acc += s.Duration
			endX := int(float64(chartWidth) * float64(acc) / float64(totalDur))
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

	// info bar — safe index clamping
	stageIdx := m.currentStage
	if stageIdx >= len(stages) {
		stageIdx = len(stages) - 1
	}
	prevRPS := 0
	if stageIdx > 0 {
		prevRPS = stages[stageIdx-1].TargetRPS
	}
	stageLbl := fmt.Sprintf("[%d/%d] %s", stageIdx+1, len(stages), stages[stageIdx].Label(prevRPS))
	stageDur := stages[stageIdx].Duration

	pausedStr := ""
	if m.planPaused {
		pausedStr = "  ⏸ PAUSED"
	}
	infoBar := fmt.Sprintf("%s  •  stage %s / %s  •  total %s / %s%s",
		stageLbl,
		formatDuration(m.stageElapsed), formatDuration(stageDur),
		formatDuration(totalElapsed), formatDuration(totalDur),
		pausedStr,
	)
	sb.WriteString("\n")
	if m.planPaused {
		sb.WriteString(cursorStyle.Render(infoBar))
	} else {
		sb.WriteString(labelStyle.Render(infoBar))
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#63")).
		Padding(0, 1).
		MarginTop(1).
		Width(m.width - 4).
		Render(sb.String())
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

		// title(3) + stats box(14) + timeline box(chartHeight=10 + title + axis + labels + arrow + NOW + info + borders = ~20) + debug(1) + margins(2)
		headerHeight := 3 + 14 + 18 + 1 + 2

		logWidth := msg.Width - 4
		if logWidth < 1 {
			logWidth = 1
		}
		logHeight := msg.Height - headerHeight - 4
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

		// Start overall clock on first tick the engine begins running.
		if m.running && !wasRunning {
			m.stageStartTime = time.Now()
		}

		// Sync currentStage from the engine (it is the authoritative source).
		if m.running && !m.planPaused {
			engineStage := m.metrics.CurrentStage
			if engineStage != m.currentStage {
				// Stage just advanced — reset the stage-elapsed clock.
				m.currentStage = engineStage
				m.stageStartTime = time.Now()
				m.stageElapsed = 0
			} else if !m.stageStartTime.IsZero() {
				m.stageElapsed = time.Since(m.stageStartTime)
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
