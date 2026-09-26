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
	Title     lipgloss.Style
	TitleText lipgloss.Style
	MetaTitle lipgloss.Style
	Muted     lipgloss.Style
	Tagline   lipgloss.Style
	Overview  lipgloss.Style
	Err       lipgloss.Style
}

func newStyles() styles {
	titleText := lipgloss.NewStyle().
		Bold(true).
		Foreground(palette.Accent)

	return styles{
		Title:     titleText.Padding(0, spInline),
		TitleText: titleText,
		MetaTitle: lipgloss.NewStyle().Foreground(palette.FgSecondary),
		Muted:     lipgloss.NewStyle().Foreground(palette.FgMuted),
		Tagline:   lipgloss.NewStyle().Foreground(palette.FgMuted).Italic(true).Width(posterCols),
		Overview: lipgloss.NewStyle().
			Foreground(palette.FgPrimary).
			Width(posterCols),
		Err: lipgloss.NewStyle().
			Foreground(palette.Error).
			Bold(true).
			Underline(true).
			Padding(0, spInline),
	}
}

func headerPad(s string) string {
	return lipgloss.NewStyle().Padding(0, spInline).Render(s)
}
