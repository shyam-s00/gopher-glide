package tui

import "github.com/charmbracelet/lipgloss"

// Theme defines the semantic color palette for the TUI. Styles reference
// these tokens by purpose (e.g. theme.Success, theme.TextMuted) rather than
// raw hex values, so the whole dashboard can be re-themed from one place.
//
// The palette is inspired by Catppuccin Macchiato.
type Theme struct {
	// Surface — panel borders, card chrome, and structural/dimmed fills.
	Surface      lipgloss.Color
	SurfaceMuted lipgloss.Color

	// Text — primary data vs. secondary labels.
	Text      lipgloss.Color
	TextMuted lipgloss.Color

	// Status — pass/fail/caution indicators used across the dashboard.
	Success lipgloss.Color
	Error   lipgloss.Color
	Warning lipgloss.Color

	// Accent — brand color, playheads, and primary highlights.
	Accent      lipgloss.Color
	AccentMuted lipgloss.Color
	OnAccent    lipgloss.Color // text rendered atop an Accent background

	// Blue — used for GET method badge.
	Blue lipgloss.Color
}

// macchiato is the default theme: a Catppuccin Macchiato-inspired palette
// mapped onto the TUI's semantic tokens.
var macchiato = Theme{
	Surface:      lipgloss.Color("#6e738d"), // Overlay0
	SurfaceMuted: lipgloss.Color("#494d64"), // Surface1

	Text:      lipgloss.Color("#cad3f5"), // Text
	TextMuted: lipgloss.Color("#a5adcb"), // Subtext0

	Success: lipgloss.Color("#a6da95"), // Green
	Error:   lipgloss.Color("#ed8796"), // Red
	Warning: lipgloss.Color("#eed49f"), // Yellow

	Accent:      lipgloss.Color("#c6a0f6"), // Mauve
	AccentMuted: lipgloss.Color("#3c3458"), // dimmed Mauve — past/inactive fills
	OnAccent:    lipgloss.Color("#24273a"), // Base

	Blue: lipgloss.Color("#8aadf4"), // Blue
}

// theme returns the active palette. Centralizing access here keeps the door
// open for future theme switching without touching call sites.
func theme() Theme {
	return macchiato
}

// ── Pre-compiled styles ───────────────────────────────────────────────────────

// uiStyles holds the complete set of pre-compiled lipgloss.Style values used
// across the dashboard. All View()-path rendering draws from here instead of
// allocating new style chains on every tick.
type uiStyles struct {
	// Spec-mandated panel standards
	PanelBorder  lipgloss.Style // fully-compiled KPI card (Width 28, margins baked in)
	PanelBase    lipgloss.Style // base rounded panel — callers apply .Width/.Height before Render
	SectionTitle lipgloss.Style // section headers and bold accent labels
	MetricLabel  lipgloss.Style // dimension labels: muted text
	MetricValue  lipgloss.Style // data values: primary text + bold

	// App chrome
	TitleBar lipgloss.Style // top banner: OnAccent text over Accent background

	// Status — bold (header counts, chart markers, bias, director bar)
	SuccessBold lipgloss.Style
	ErrorBold   lipgloss.Style
	Highlight   lipgloss.Style // Warning + bold: cursor, active-actors, director messages

	// Status — regular (chart trend lines, log entry badges)
	Success lipgloss.Style
	Error   lipgloss.Style
	Body    lipgloss.Style // neutral body text: log entry fields

	// Timeline chart fills
	PastBar     lipgloss.Style
	PastEmpty   lipgloss.Style
	FutureBar   lipgloss.Style
	FutureEmpty lipgloss.Style
	Boundary    lipgloss.Style

	// HTTP method badges
	BadgeGET    lipgloss.Style
	BadgePOST   lipgloss.Style
	BadgeDELETE lipgloss.Style
	BadgeMethod lipgloss.Style // fallback for unrecognised methods

	// HTTP status badges
	Badge2xx   lipgloss.Style
	Badge4xx   lipgloss.Style
	Badge5xx   lipgloss.Style
	BadgeError lipgloss.Style // non-HTTP / network errors
}

func newStyles(th Theme) uiStyles {
	panelBase := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.Surface).
		Padding(0, 1).
		MarginTop(1)

	return uiStyles{
		PanelBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(th.Surface).
			Padding(0, 1),
		PanelBase:    panelBase,
		SectionTitle: lipgloss.NewStyle().Bold(true).Foreground(th.Accent),
		MetricLabel:  lipgloss.NewStyle().Foreground(th.TextMuted),
		MetricValue:  lipgloss.NewStyle().Foreground(th.Text).Bold(true),

		TitleBar: lipgloss.NewStyle().
			Bold(true).
			Foreground(th.OnAccent).
			Background(th.Accent).
			Padding(0, 1).
			MarginTop(1).
			MarginBottom(1),

		SuccessBold: lipgloss.NewStyle().Foreground(th.Success).Bold(true),
		ErrorBold:   lipgloss.NewStyle().Foreground(th.Error).Bold(true),
		Highlight:   lipgloss.NewStyle().Foreground(th.Warning).Bold(true),

		Success: lipgloss.NewStyle().Foreground(th.Success),
		Error:   lipgloss.NewStyle().Foreground(th.Error),
		Body:    lipgloss.NewStyle().Foreground(th.Text),

		PastBar:     lipgloss.NewStyle().Foreground(th.Accent),
		PastEmpty:   lipgloss.NewStyle().Foreground(th.AccentMuted),
		FutureBar:   lipgloss.NewStyle().Foreground(th.SurfaceMuted),
		FutureEmpty: lipgloss.NewStyle().Foreground(th.SurfaceMuted).Faint(true),
		Boundary:    lipgloss.NewStyle().Foreground(th.Surface),

		BadgeGET:    lipgloss.NewStyle().Background(th.Blue).Foreground(th.OnAccent).Bold(true).Padding(0, 1),
		BadgePOST:   lipgloss.NewStyle().Background(th.Success).Foreground(th.OnAccent).Bold(true).Padding(0, 1),
		BadgeDELETE: lipgloss.NewStyle().Background(th.Error).Foreground(th.OnAccent).Bold(true).Padding(0, 1),
		BadgeMethod: lipgloss.NewStyle().Background(th.SurfaceMuted).Foreground(th.Text).Bold(true).Padding(0, 1),

		Badge2xx:   lipgloss.NewStyle().Background(th.Success).Foreground(th.OnAccent).Bold(true).Padding(0, 1),
		Badge4xx:   lipgloss.NewStyle().Background(th.Warning).Foreground(th.OnAccent).Bold(true).Padding(0, 1),
		Badge5xx:   lipgloss.NewStyle().Background(th.Error).Foreground(th.OnAccent).Bold(true).Padding(0, 1),
		BadgeError: lipgloss.NewStyle().Background(th.Error).Foreground(th.OnAccent).Bold(true).Padding(0, 1),
	}
}

// styles is the active pre-compiled palette, initialised once at startup from
// the default theme. Re-assign styles = newStyles(myTheme) to hot-swap palettes.
var styles = newStyles(macchiato)
