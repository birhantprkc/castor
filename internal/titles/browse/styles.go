package browse

import (
	"charm.land/lipgloss/v2"
	"github.com/stupside/castor/internal/palette"
)

const (
	spInline = 2
	spGutter = 2
)

type styles struct {
	title     lipgloss.Style
	titleText lipgloss.Style
	metaTitle lipgloss.Style
	accent    lipgloss.Style
	muted     lipgloss.Style
	tagline   lipgloss.Style
	overview  lipgloss.Style
	err       lipgloss.Style
	box       lipgloss.Style
}

func newStyles(p palette.Palette) styles {
	titleText := lipgloss.NewStyle().
		Bold(true).
		Foreground(p.Accent)

	return styles{
		title:     titleText.Padding(0, spInline),
		titleText: titleText,
		metaTitle: lipgloss.NewStyle().Foreground(p.FgSecondary),
		accent:    lipgloss.NewStyle().Foreground(p.Accent),
		muted:     lipgloss.NewStyle().Foreground(p.FgMuted),
		tagline:   lipgloss.NewStyle().Foreground(p.FgMuted).Italic(true).Width(posterCols),
		overview: lipgloss.NewStyle().
			Foreground(p.FgPrimary).
			Width(posterCols),
		err: lipgloss.NewStyle().
			Foreground(p.Error).
			Bold(true).
			Underline(true).
			Padding(0, spInline),
		box: lipgloss.NewStyle().
			Padding(1, 2).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.Accent),
	}
}

func headerPad(s string) string {
	return lipgloss.NewStyle().Padding(0, spInline).Render(s)
}
