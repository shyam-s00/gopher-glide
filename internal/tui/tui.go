package tui

import (
	"context"
	"fmt"
	"gopher-glide/internal/config"
	"gopher-glide/internal/engine"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type model struct {
	logView viewport.Model
	engine  *engine.Engine
	config  *config.Config
	metrics *engine.MetricsSnapshot
	ctx     context.Context
	cancel  context.CancelFunc
	ready   bool
	running bool
	//startTime time.Time
	width        int
	height       int
	showFailures bool
}

type tickMsg time.Time

func initialModel(eng *engine.Engine, cfg *config.Config) model {
	ctx, cancel := context.WithCancel(context.Background())

	vp := viewport.New(0, 0)
	vp.YPosition = 0
	//vp.SetContent("Initializing ....")

	return model{
		engine:  eng,
		config:  cfg,
		metrics: &engine.MetricsSnapshot{},
		ctx:     ctx,
		cancel:  cancel,
		running: false,
		//startTime: time.Now(),
		showFailures: true,
		logView:      vp,
	}
}

func (m model) Init() tea.Cmd {
	go func() {
		targetRPS := 300
		duration := time.Second * 10
		url := "https://httpbin.org/get"

		_ = m.engine.Run(m.ctx, targetRPS, duration, url)
	}()

	return tea.Batch(tickCmd(), tea.EnterAltScreen)
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) renderHeader() string {

	elapsed := m.engine.GetElapsedTime()
	//appStyle := lipgloss.NewStyle().Padding(0, 2)

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
		MarginRight(2).
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

	configuration := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("CONFIGURATION"),
		labelStyle.Render("Status:"), lipgloss.NewStyle().Foreground(statusColor).Bold(true).Render(statusStr),
		labelStyle.Render("Uptime:"), valueStyle.Render(fmt.Sprintf("%.2fs", elapsed)),
		labelStyle.Render("Http File:"), valueStyle.Render(m.config.ConfigSection.HTTPFile),
		labelStyle.Render("Active VPU:"), valueStyle.Render(fmt.Sprintf("%d", m.metrics.ActiveVPUs)),
		labelStyle.Render("Target RPS:"), valueStyle.Render(fmt.Sprintf("%d", m.metrics.TargetRPS)),
		"")

	throughput := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("THROUGHPUT"),
		labelStyle.Render("RPS:"), valueStyle.Render(fmt.Sprintf("%.2f", m.metrics.Throughput)),
		labelStyle.Render("Total Requests:"), valueStyle.Render(fmt.Sprintf("%d", m.metrics.TotalRequests)),
		labelStyle.Render("Success:"), successStyle.Render(fmt.Sprintf("%d", m.metrics.SuccessCount)),
		labelStyle.Render("Failed:"), errorStyle.Render(fmt.Sprintf("%d", m.metrics.FailureCount)),
		labelStyle.Render("ErrorRate:"), valueStyle.Render(fmt.Sprintf("%.2f%%", m.metrics.ErrorRate*100)),
		"")

	latency := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("LATENCY"),
		labelStyle.Render("Min:"), valueStyle.Render(fmt.Sprintf("%.2f", m.metrics.MinLatency)),
		labelStyle.Render("Max:"), valueStyle.Render(fmt.Sprintf("%.2f", m.metrics.MaxLatency)),
		labelStyle.Render("P50:"), valueStyle.Render(fmt.Sprintf("%.2f", m.metrics.P50Latency)),
		labelStyle.Render("P95:"), valueStyle.Render(fmt.Sprintf("%.2f", m.metrics.P95Latency)),
		labelStyle.Render("P99:"), valueStyle.Render(fmt.Sprintf("%.2f", m.metrics.P99Latency)),
		"")

	header := titleStyle.Render("Gopher Glide (GG) -  Load Test")
	stats := lipgloss.JoinHorizontal(lipgloss.Top,
		boxStyle.Render(configuration),
		boxStyle.Render(throughput),
		boxStyle.Render(latency),
	)

	return lipgloss.JoinVertical(lipgloss.Left, header, stats)
}

func (m model) renderLogContent() string {
	var logs []engine.CallLog
	if m.showFailures {
		logs = m.engine.GetRecentErrorLogs(100)
	} else {
		logs = m.engine.GetRecentLogs(100)
	}

	//Styles
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

		// title (3) + stats (10) + debug header (3) + extra margins
		headerHeight := 20

		logWidth := msg.Width - 4
		if logWidth < 1 {
			logWidth = 1
		}

		// Subtract header height, top margin(1), and 2 lines for a border
		logHeight := msg.Height - headerHeight - 3
		if logHeight < 1 {
			logHeight = 1
		}

		m.logView.Width = logWidth
		m.logView.Height = logHeight
		m.logView.SetContent(m.renderLogContent())
	case tickMsg:
		// Update the running status from the engine
		m.running = m.engine.IsRunning()
		m.metrics = m.engine.GetMetrics()

		if m.ready {
			atBottom := m.logView.AtBottom()
			m.logView.SetContent(m.renderLogContent())
			if atBottom {
				m.logView.GotoBottom()
			}
		}
		cmds = append(cmds, tickCmd())
	}
	// update the log view to handle scrolling if needed
	m.logView, cmd = m.logView.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m model) View() string {
	if !m.ready {
		return "Loading..."
	}

	header := m.renderHeader()

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
		Height(m.logView.Height). // Height sets content height, borders are added outside
		Render(m.logView.View())

	return lipgloss.JoinVertical(lipgloss.Left, header, debugHeader, logBox)
}

func Start(eng *engine.Engine, cfg *config.Config) error {
	p := tea.NewProgram(
		initialModel(eng, cfg),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	_, err := p.Run()
	return err
}
