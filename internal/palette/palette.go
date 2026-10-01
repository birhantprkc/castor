// Package palette is the look every castor screen shares.
package palette

import (
	"image/color"

	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
)

type Palette struct {
	Dark        bool
	Accent      color.Color
	FgPrimary   color.Color
	FgSecondary color.Color
	FgMuted     color.Color
	Error       color.Color
	// Bar is a header or footer band; Rule a separator drawn on it.
	Bar  color.Color
	Rule color.Color
}

// New resolves the palette for the terminal background the screen reported.
func New(dark bool) Palette {
	pick := lipgloss.LightDark(dark)
	return Palette{
		Dark:        dark,
		Accent:      pick(lipgloss.Color("#6366F1"), lipgloss.Color("#818CF8")),
		FgPrimary:   pick(lipgloss.Color("#18181B"), lipgloss.Color("#FAFAFA")),
		FgSecondary: pick(lipgloss.Color("#52525B"), lipgloss.Color("#D4D4D8")),
		FgMuted:     pick(lipgloss.Color("#A1A1AA"), lipgloss.Color("#52525B")),
		Error:       pick(lipgloss.Color("#DC2626"), lipgloss.Color("#FCA5A5")),
		Bar:         pick(lipgloss.Color("#F4F4F5"), lipgloss.Color("#27272A")),
		Rule:        pick(lipgloss.Color("#D4D4D8"), lipgloss.Color("#3F3F46")),
	}
}

// Delegate draws the rows of every castor list.
func (p Palette) Delegate() list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	s := list.NewDefaultItemStyles(p.Dark)
	s.NormalTitle = s.NormalTitle.Foreground(p.FgPrimary)
	s.NormalDesc = s.NormalDesc.Foreground(p.FgMuted)
	s.SelectedTitle = s.SelectedTitle.Foreground(p.Accent).BorderForeground(p.Accent).Bold(true)
	s.SelectedDesc = s.SelectedDesc.Foreground(p.FgSecondary).BorderForeground(p.Accent)
	s.DimmedTitle = s.DimmedTitle.Foreground(p.FgMuted)
	s.DimmedDesc = s.DimmedDesc.Foreground(p.FgMuted)
	s.FilterMatch = lipgloss.NewStyle().Foreground(p.Accent).Underline(true)
	d.Styles = s
	return d
}

// StyleList restyles l for the background; list.New fixes its styles to dark and never styles its filter input.
func (p Palette) StyleList(l *list.Model) {
	l.Styles = list.DefaultStyles(p.Dark)
	l.Paginator.ActiveDot = l.Styles.ActivePaginationDot.String()
	l.Paginator.InactiveDot = l.Styles.InactivePaginationDot.String()
	l.FilterInput.SetStyles(l.Styles.Filter)
}
