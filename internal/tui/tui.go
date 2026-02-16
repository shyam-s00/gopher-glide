package tui

import (
	"context"
	"fmt"
	"gopher-glide/internal/config"
	"gopher-glide/internal/engine"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type model struct {
	viewport viewport.Model
	engine   *engine.Engine
	config   *config.Config
	metrics  *engine.MetricsSnapshot
	ctx      context.Context
	cancel   context.CancelFunc
	ready    bool
	running  bool
	//startTime time.Time
	width  int
	height int
}

type tickMsg time.Time

func initialModel(eng *engine.Engine, cfg *config.Config) model {
	ctx, cancel := context.WithCancel(context.Background())

	vp := viewport.New(80, 24)
	vp.SetContent("Initializing ....")

	return model{
		engine:  eng,
		config:  cfg,
		metrics: &engine.MetricsSnapshot{},
		ctx:     ctx,
		cancel:  cancel,
		running: false,
		//startTime: time.Now(),
		viewport: vp,
	}
}

func (m model) Init() tea.Cmd {
	go func() {
		targetVPU := 250
		duration := time.Second * 10
		url := "https://httpbin.org/get"

		_ = m.engine.Run(m.ctx, targetVPU, duration, url)
	}()

	return tea.Batch(tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) renderContent() string {
	elapsed := m.engine.GetElapsedTime()

	appStyle := lipgloss.NewStyle().Padding(0, 2)

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

	debugStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#63")).
		Padding(0, 1).
		MarginTop(1).
		Width(94)

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

	header := titleStyle.Render("Gopher Glide (GG) -  Load Test")

	//uptime := time.Since(m.startTime).Round(time.Second)
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
		labelStyle.Render("Active VPU:"), valueStyle.Render(fmt.Sprintf("%d / %d Target", m.metrics.ActiveVPUs, m.metrics.CurrentVPUs)),
		"", "", "")

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

	panel1 := boxStyle.Render(configuration)
	panel2 := boxStyle.Render(throughput)
	panel3 := boxStyle.Render(latency)

	body := lipgloss.JoinHorizontal(lipgloss.Top, panel1, panel2, panel3)

	debugContent := m.renderDebugPanel()
	debugPanel := debugStyle.Render(debugContent)

	help := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#A0A0A0")).
		Render("Press 'q' to quit")

	return appStyle.Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			header, body, debugPanel, help))

}

func (m model) renderDebugPanel() string {
	sectionStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7D56F4"))

	successStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#04B575"))

	errorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FF5F87"))

	normalStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))

	logs := m.engine.GetRecentLogs(10)

	var lines []string
	lines = append(lines, sectionStyle.Render("[DEBUG]Recent Logs"))
	lines = append(lines, "")

	if len(logs) == 0 {
		lines = append(lines, normalStyle.Render("No logs available"))
	} else {
		for _, log := range logs {
			timestamp := log.Timestamp.Format("2006-01-02 15:04:05")
			duration := fmt.Sprintf("%.0fms", log.Duration.Seconds()/1000)

			var statusStr string
			var statusStyle lipgloss.Style

			if log.Error != "" {
				statusStr = fmt.Sprintf("[ERROR] %s", log.Error)
				statusStyle = errorStyle
			} else if log.StatusCode >= 200 && log.StatusCode < 300 {
				statusStr = successStyle.Render(fmt.Sprintf("[%d]", log.StatusCode))
				statusStyle = successStyle
			} else {
				statusStr = errorStyle.Render(fmt.Sprintf("[%d]", log.StatusCode))
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
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.cancel()
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport = viewport.New(msg.Width, msg.Height)
		m.viewport.SetContent(m.renderContent())
		m.ready = true
		return m, nil

	case tickMsg:
		// Update the running status from the engine
		m.running = m.engine.IsRunning()
		m.metrics = m.engine.GetMetrics()

		//elapsed := time.Since(m.startTime).Seconds()
		//if elapsed > 0 && m.metrics.TotalRequests > 0 {
		//	m.metrics.Throughput = float64(m.metrics.TotalRequests) / elapsed
		//}

		if m.ready && m.viewport.Width > 0 {
			m.viewport.SetContent(m.renderContent())
		}

		return m, tickCmd()
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m model) View() string {
	if !m.ready {
		return "Loading..."
	}

	return m.viewport.View()
}

func Start(eng *engine.Engine, cfg *config.Config) error {
	p := tea.NewProgram(
		initialModel(eng, cfg),
		tea.WithAltScreen(),
	)

	_, err := p.Run()
	return err
}
