package browse

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
)

// Horizontal spacing: the inset of every row, and the gap between the results and the inspector.
const (
	spInline = 2
	spGutter = 2
)

func headerPad(s string) string {
	return lipgloss.NewStyle().Padding(0, spInline).Render(s)
}

// truncate fits s in width cells, ending it with an ellipsis when cut.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}

func blankRect(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	return strings.Join(slices.Repeat([]string{strings.Repeat(" ", w)}, h), "\n")
}

// clampRows forces s to exactly n lines; truncate or pad with blanks.
func clampRows(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	// n-len(lines) cannot go negative: the truncation above leaves at most n lines.
	lines = append(lines, make([]string, n-len(lines))...)
	return strings.Join(lines, "\n")
}
