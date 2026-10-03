package browse

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"

	"github.com/stupside/castor/cmd/castor/internal/palette"
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

func newHelpStyles(p palette.Palette) help.Styles {
	s := help.DefaultStyles(p.Dark)
	s.ShortKey = s.ShortKey.Foreground(p.FgSecondary)
	s.ShortDesc = s.ShortDesc.Foreground(p.FgMuted)
	s.ShortSeparator = s.ShortSeparator.Foreground(p.FgMuted)
	s.FullKey = s.FullKey.Foreground(p.FgSecondary)
	s.FullDesc = s.FullDesc.Foreground(p.FgMuted)
	s.FullSeparator = s.FullSeparator.Foreground(p.FgMuted)
	s.Ellipsis = s.Ellipsis.Foreground(p.FgMuted)
	return s
}

func newQueryStyles(p palette.Palette) textinput.Styles {
	s := textinput.DefaultStyles(p.Dark)
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(p.Accent)
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(p.FgMuted)
	s.Focused.Text = lipgloss.NewStyle().Foreground(p.FgPrimary)
	s.Cursor.Color = lipgloss.NoColor{} // terminal reverse video, not v2's grey tint
	return s
}
