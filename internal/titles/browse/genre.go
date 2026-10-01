package browse

import (
	"context"
	"fmt"
	"slices"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stupside/castor/internal/palette"
	"github.com/stupside/castor/internal/titles/tmdb"
)

// GenrePicker is the modal genre filter; owns draft selection, isolated from feed.
type genrePicker struct {
	list     list.Model
	styles   styles
	catalog  tmdb.GenreCatalog
	loaded   bool
	media    string       // tmdb.MediaMovie | tmdb.MediaTV
	selected map[int]bool // genre id -> chosen, for the current media type
	shown    bool
}

// GenreAction is the result of a key press to the picker.
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
		selected: map[int]bool{},
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
			g.selected[it.g.ID] = !g.selected[it.g.ID]
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
		items[i] = genreItem{g: gr, selected: g.selected[gr.ID]}
	}
	g.list.SetItems(items)
	// TV catalogue shorter; list.Select doesn't bound-check; clamp cursor.
	g.list.Select(min(idx, max(len(items)-1, 0)))
}

// genreIDs is in a stable order.
func (g genrePicker) genreIDs() []int {
	ids := make([]int, 0, len(g.selected))
	for id, on := range g.selected {
		if on {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// selectedNames is in catalogue order.
func (g genrePicker) selectedNames() []string {
	var names []string
	for _, gr := range g.catalog.For(g.media) {
		if g.selected[gr.ID] {
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

func (g genrePicker) view(spin spinner.Model, w, h int) string {
	title := lipgloss.JoinHorizontal(lipgloss.Left,
		g.styles.titleText.Render("Filter by genre"),
		g.styles.muted.Render("  ·  "+g.mediaLabel()),
	)

	body := g.styles.muted.Render(spin.View() + " loading genres…")
	if g.loaded {
		body = g.list.View()
	}

	count := "no genres selected"
	if n := len(g.genreIDs()); n > 0 {
		count = fmt.Sprintf("%d selected", n)
	}

	hints := g.styles.muted.Render(
		"space toggle · ↵ apply · m movies/tv · c clear · esc cancel",
	)

	content := lipgloss.JoinVertical(lipgloss.Left,
		title, "", body, "",
		g.styles.muted.Render(count), hints,
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
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetSpacing(0)
	d.Styles = p.ItemStyles()
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
