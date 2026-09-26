// Package palette is the look every castor screen shares.
package palette

import "github.com/charmbracelet/lipgloss"

var (
	Accent      = lipgloss.AdaptiveColor{Light: "#6366F1", Dark: "#818CF8"}
	FgPrimary   = lipgloss.AdaptiveColor{Light: "#18181B", Dark: "#FAFAFA"}
	FgSecondary = lipgloss.AdaptiveColor{Light: "#52525B", Dark: "#D4D4D8"}
	FgMuted     = lipgloss.AdaptiveColor{Light: "#A1A1AA", Dark: "#52525B"}
	Error       = lipgloss.AdaptiveColor{Light: "#DC2626", Dark: "#FCA5A5"}
)
