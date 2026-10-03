package browse

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stupside/castor/tui/internal/browse/tmdb"
	"github.com/stupside/castor/tui/internal/palette"
)

// genrePicker is the modal genre filter; owns draft selection, isolated from feed.
type genrePicker struct {
	list     list.Model
	styles   styles
	catalog  tmdb.GenreCatalog
	loaded   bool
	media    tmdb.Media
	selected map[int]struct{} // genre ids chosen for the current media
	shown    bool
}

// genreAction is the result of a key press to the picker.
type genreAction int

const (
	genreIdle      genreAction = iota // still open; nothing for the model to do
	genreCancelled                    // closed without applying
	genreApplied                      // closed; run a discover query
)

func newGenrePicker() genrePicker {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(true)
	l.SetFilteringEnabled(false) // bare letters drive picker commands
	// DisableQuitKeybindings() survives state changes; SetEnabled() reverts.
	l.DisableQuitKeybindings()

	return genrePicker{
		list:     l,
		media:    tmdb.MediaMovie,
		selected: map[int]struct{}{},
	}
}

func (g *genrePicker) restyle(p palette.Palette, st styles) {
	g.styles = st
	g.list.SetDelegate(newGenreDelegate(p))
	p.StyleList(&g.list)
}

// setCatalog refreshes checklist if modal is open before fetch completes.
func (g *genrePicker) setCatalog(cat tmdb.GenreCatalog, w, h int) {
	g.catalog = cat
	g.loaded = true
	if g.shown {
		g.resize(w, h) // open() skipped it: there was no catalogue to size the list against
		g.reload()
	}
}

func (g *genrePicker) open(w, h int) {
	g.shown = true
	if g.loaded {
		g.resize(w, h)
		g.reload()
	}
}

func (g *genrePicker) resize(w, h int) {
	rows := min(len(g.catalog.For(g.media)), max(h-10, 6))
	width := min(max(w-8, 24), 48)
	g.list.SetSize(width, max(rows, 1))
}

func (g *genrePicker) update(msg tea.KeyPressMsg, keys keyMap, w, h int) (tea.Cmd, genreAction) {
	switch {
	case key.Matches(msg, keys.back):
		g.shown = false
		return nil, genreCancelled
	case key.Matches(msg, keys.enter):
		g.shown = false
		return nil, genreApplied
	case key.Matches(msg, keys.space):
		if it, ok := g.list.SelectedItem().(genreItem); ok {
			if _, on := g.selected[it.g.ID]; on {
				delete(g.selected, it.g.ID)
			} else {
				g.selected[it.g.ID] = struct{}{}
			}
			g.reload()
		}
		return nil, genreIdle
	case key.Matches(msg, keys.clearGenres):
		clear(g.selected)
		g.reload()
		return nil, genreIdle
	case key.Matches(msg, keys.overlayMedia):
		g.toggleMedia(w, h)
		return nil, genreIdle
	}
	var cmd tea.Cmd
	g.list, cmd = g.list.Update(msg)
	return cmd, genreIdle
}

// toggleMedia clears the draft selection, because genre ids are namespaced per media type.
func (g *genrePicker) toggleMedia(w, h int) {
	if g.media == tmdb.MediaTV {
		g.media = tmdb.MediaMovie
	} else {
		g.media = tmdb.MediaTV
	}
	clear(g.selected)
	if g.loaded {
		g.resize(w, h)
		g.reload()
	}
}

func (g *genrePicker) reload() {
	idx := g.list.Index()
	genres := g.catalog.For(g.media)
	items := make([]list.Item, len(genres))
	for i, gr := range genres {
		_, on := g.selected[gr.ID]
		items[i] = genreItem{g: gr, selected: on}
	}
	g.list.SetItems(items)
	// TV catalogue shorter; list.Select doesn't bound-check; clamp cursor.
	g.list.Select(min(idx, max(len(items)-1, 0)))
}

func (g genrePicker) genreIDs() []int { return slices.Sorted(maps.Keys(g.selected)) }

// selectedNames is in catalogue order.
func (g genrePicker) selectedNames() []string {
	var names []string
	for _, gr := range g.catalog.For(g.media) {
		if _, on := g.selected[gr.ID]; on {
			names = append(names, gr.Name)
		}
	}
	return names
}

func (g genrePicker) mediaLabel() string {
	if g.media == tmdb.MediaTV {
		return "TV Shows"
	}
	return "Movies"
}

func (g genrePicker) view(spin spinner.Model, hints help.Model, keys keyMap, w, h int) string {
	title := lipgloss.JoinHorizontal(lipgloss.Left,
		g.styles.titleText.Render("Filter by genre"),
		g.styles.muted.Render("  ·  "+g.mediaLabel()),
	)

	body := g.styles.muted.Render(spin.View() + " loading genres…")
	if g.loaded {
		body = g.list.View()
	}

	count := "no genres selected"
	if n := len(g.selected); n > 0 {
		count = fmt.Sprintf("%d selected", n)
	}

	apply, cancel := keys.enter, keys.back
	apply.SetHelp("↵", "apply")
	cancel.SetHelp("esc", "cancel")

	content := lipgloss.JoinVertical(lipgloss.Left,
		title, "", body, "",
		g.styles.muted.Render(count),
		hints.ShortHelpView([]key.Binding{keys.space, apply, keys.overlayMedia, keys.clearGenres, cancel}),
	)
	box := g.styles.box.Render(content)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

type genreItem struct {
	g        tmdb.Genre
	selected bool
}

func (i genreItem) Title() string {
	box := "○"
	if i.selected {
		box = "◉"
	}
	return box + "  " + i.g.Name
}

func (i genreItem) Description() string { return "" }
func (i genreItem) FilterValue() string { return i.g.Name }

func newGenreDelegate(p palette.Palette) list.DefaultDelegate {
	d := p.Delegate()
	d.ShowDescription = false
	d.SetSpacing(0)
	return d
}

type genresLoadedMsg struct {
	cat tmdb.GenreCatalog
	err error
}

func loadGenresCmd(ctx context.Context, c Catalog) tea.Cmd {
	return func() tea.Msg {
		cat, err := c.Genres(ctx)
		return genresLoadedMsg{cat: cat, err: err}
	}
}
