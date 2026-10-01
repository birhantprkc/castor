package browse

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/stupside/castor/internal/browse/palette"
)

const (
	spInline = 2
	spGutter = 2
)

type styles struct {
	title     lipgloss.Style
	titleText lipgloss.Style
	metaTitle lipgloss.Style
	muted     lipgloss.Style
	tagline   lipgloss.Style
	overview  lipgloss.Style
	err       lipgloss.Style
}

func newStyles() styles {
	titleText := lipgloss.NewStyle().
		Bold(true).
		Foreground(palette.Accent)

	return styles{
		title:     titleText.Padding(0, spInline),
		titleText: titleText,
		metaTitle: lipgloss.NewStyle().Foreground(palette.FgSecondary),
		muted:     lipgloss.NewStyle().Foreground(palette.FgMuted),
		tagline:   lipgloss.NewStyle().Foreground(palette.FgMuted).Italic(true).Width(posterCols),
		overview: lipgloss.NewStyle().
			Foreground(palette.FgPrimary).
			Width(posterCols),
		err: lipgloss.NewStyle().
			Foreground(palette.Error).
			Bold(true).
			Underline(true).
			Padding(0, spInline),
	}
}

func headerPad(s string) string {
	return lipgloss.NewStyle().Padding(0, spInline).Render(s)
}
