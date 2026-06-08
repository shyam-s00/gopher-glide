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
}

// theme returns the active palette. Centralizing access here keeps the door
// open for future theme switching without touching call sites.
func theme() Theme {
	return macchiato
}
