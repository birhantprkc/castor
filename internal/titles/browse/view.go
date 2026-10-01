package browse

import (
	"strings"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/stupside/castor/internal/palette"
)

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

// bodyHeight is list/poster row height (total minus footer and chrome above).
func (m model) bodyHeight() int {
	chrome := 3 // drilldown: header + 2 blanks
	if m.scr == screenBrowse {
		chrome = 4 // header + query + 2 blanks
		if m.mode == modeDiscover && m.query.Value() == "" {
			chrome++ // filter bar
		}
	}
	return max(m.h-lipgloss.Height(m.footer())-chrome, 8)
}

func (m model) View() tea.View {
	v := tea.NewView(m.content())
	v.AltScreen = true
	return v
}

func (m model) content() string {
	switch {
	case m.picker.shown:
		return m.picker.view(m.spin, m.w, m.h)
	case m.scr == screenDrilldown:
		return lipgloss.JoinVertical(lipgloss.Left, m.drill.view(m.styles), "", m.footer())
	default:
		return m.viewBrowse()
	}
}

func (m model) viewBrowse() string {
	rows := []string{m.browseHeader(), m.query.View()}
	if m.mode == modeDiscover && m.query.Value() == "" {
		rows = append(rows, m.filterBar())
	}
	rows = append(rows, "", m.bodyRow(), "", m.footer())
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// browseHeader renders active label (left), mode/cast target (right).
func (m model) browseHeader() string {
	var label, rhs string
	switch {
	case m.query.Value() != "":
		label = "Search"
	case m.mode == modeDiscover:
		label = "Discover"
		rhs = m.styles.muted.Render(m.picker.mediaLabel())
	default:
		label = m.tab.label()
		rhs = m.renderTabDots()
	}
	if rhs != "" {
		rhs += "  "
	}
	rhs += m.styles.muted.Render(m.devBadge)
	title := m.styles.title.Render(label)
	placed := lipgloss.PlaceHorizontal(max(m.w-lipgloss.Width(title), 0), lipgloss.Right, rhs)
	return lipgloss.JoinHorizontal(lipgloss.Top, title, placed)
}

func (m model) filterBar() string {
	summary := "All genres"
	if names := m.picker.selectedNames(); len(names) > 0 {
		summary = strings.Join(names, ", ")
	}
	left := lipgloss.NewStyle().Padding(0, spInline).Render(
		m.styles.metaTitle.Render(truncate(summary, max(m.w/2, 12))) +
			m.styles.muted.Render("  ·  Sort: "+sortLabel(m.disc.sort)),
	)
	hints := m.help.Styles.ShortDesc.Render("^g genres · ^s sort · ^t movie/tv")
	rhs := lipgloss.PlaceHorizontal(max(m.w-lipgloss.Width(left), 0), lipgloss.Right, hints)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, rhs)
}

func (m model) renderTabDots() string {
	parts := make([]string, tabCount)
	for i := range tabCount {
		if i == m.tab {
			parts[i] = m.styles.accent.Render("●")
		} else {
			parts[i] = m.styles.muted.Render("○")
		}
	}
	return strings.Join(parts, " ")
}

func (m model) bodyRow() string {
	h := m.bodyHeight()
	left := m.results.View()
	right := m.inspector.view(m.selectedResult(), h)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", spGutter), right)
}

// footer always renders both rows to prevent body-height jitter during transitions.
func (m model) footer() string {
	pad := lipgloss.NewStyle().Padding(0, spInline)
	helpLine := pad.Render(m.help.View(screenKeys{k: m.keys, s: m.scr, discover: m.mode == modeDiscover}))
	status := m.statusLine()
	if status == "" {
		status = " " // reserve the row
	}
	return lipgloss.JoinVertical(lipgloss.Left, helpLine, status)
}

func (m model) statusLine() string {
	pad := lipgloss.NewStyle().Padding(0, spInline)
	switch {
	case m.loading:
		return pad.Render(m.spin.View() + " " + m.styles.muted.Render("loading…"))
	case m.err != nil:
		return m.styles.err.Render("error: " + m.err.Error())
	}
	return ""
}
