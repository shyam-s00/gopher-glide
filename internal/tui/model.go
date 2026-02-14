package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Model struct {
	testName     string
	startTime    time.Time
	currentStage int
	totalStages  int

	currentRPS    float64
	activeVPUs    int
	targetVPUs    int
	totalRequests int
	successCount  int
	failureCount  int

	// latency (ms)
	minLatency float64
	maxLatency float64
	p50Latency float64
	p95Latency float64
	p99Latency float64

	// current status
	running bool
	width   int
	height  int
	err     error
}

func InitialModel() Model {
	return Model{
		testName:     "Gopher Glide Load Test (Placeholder)",
		startTime:    time.Now(),
		currentStage: 1,
		totalStages:  5,
		running:      true,
		activeVPUs:   10,
		targetVPUs:   50,

		// Placeholders
		currentRPS:    124.5,
		totalRequests: 1024,
		successCount:  1000,
		failureCount:  24,
		minLatency:    12.5,
		maxLatency:    450.2,
		p50Latency:    45.1,
		p95Latency:    120.4,
		p99Latency:    210.8,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tea.Tick(time.Second, func(t time.Time) tea.Msg {
			return tickMsg(t)
		}),
	)
}

type tickMsg time.Time

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		if m.running {
			// Simulate updates for the placeholder
			m.totalRequests += 10
			m.successCount += 9
			m.failureCount += 1
			m.currentRPS += 0.5
			if m.currentRPS > 200 {
				m.currentRPS = 100
			}
			return m, tea.Tick(time.Second, func(t time.Time) tea.Msg {
				return tickMsg(t)
			})
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.err != nil {
		return fmt.Sprintf("Error: %v\n", m.err)
	}

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

	header := titleStyle.Render(m.testName)

	uptime := time.Since(m.startTime).Round(time.Second)
	statusStr := "STOPPED"
	statusColor := lipgloss.Color("#FF5F87")

	if m.running {
		statusStr = "RUNNING"
		statusColor = lipgloss.Color("#04B575")
	}

	config := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("CONFIGURATION"),
		labelStyle.Render("Status:"), lipgloss.NewStyle().Foreground(statusColor).Bold(true).Render(statusStr),
		labelStyle.Render("Uptime:"), valueStyle.Render(uptime.String()),
		labelStyle.Render("Stage:"), valueStyle.Render(fmt.Sprintf("%d/%d", m.currentStage, m.totalStages)),
		labelStyle.Render("Active VPU:"), valueStyle.Render(fmt.Sprintf("%d / %d Target", m.activeVPUs, m.targetVPUs)),
		"", "")

	throughput := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("THROUGHPUT"),
		labelStyle.Render("RPS:"), valueStyle.Render(fmt.Sprintf("%.2f", m.currentRPS)),
		labelStyle.Render("Total Requests:"), valueStyle.Render(fmt.Sprintf("%d", m.totalRequests)),
		labelStyle.Render("Success:"), successStyle.Render(fmt.Sprintf("%d", m.successCount)),
		labelStyle.Render("Failed:"), errorStyle.Render(fmt.Sprintf("%d", m.failureCount)),
		"", "")

	latency := lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render("LATENCY"),
		labelStyle.Render("Min:"), valueStyle.Render(fmt.Sprintf("%.2f", m.minLatency)),
		labelStyle.Render("Max:"), valueStyle.Render(fmt.Sprintf("%.2f", m.maxLatency)),
		labelStyle.Render("P50:"), valueStyle.Render(fmt.Sprintf("%.2f", m.p50Latency)),
		labelStyle.Render("P95:"), valueStyle.Render(fmt.Sprintf("%.2f", m.p95Latency)),
		labelStyle.Render("P99:"), valueStyle.Render(fmt.Sprintf("%.2f", m.p99Latency)))

	panel1 := boxStyle.Render(config)
	panel2 := boxStyle.Render(throughput)
	panel3 := boxStyle.Render(latency)

	body := lipgloss.JoinHorizontal(lipgloss.Top, panel1, panel2, panel3)

	help := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#A0A0A0")).
		Render("Press 'q' to quit")

	return appStyle.Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			header, body, help),
	)

}

func Start() error {
	p := tea.NewProgram(InitialModel())
	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}
