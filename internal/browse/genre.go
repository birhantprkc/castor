package browse

import (
	"context"
	"fmt"
	"slices"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/stupside/castor/internal/browse/tmdb"
)

// genrePicker is the modal genre filter. It owns the draft filter (which media
// type and which genre ids are selected) and the checklist that edits it. The
// discover feed reads mediaType()/genreIDs() when it needs to run a query; the
// picker never touches the feed itself.
type genrePicker struct {
	list     list.Model
	styles   styles
	help     help.Model
	catalog  tmdb.GenreCatalog
	loaded   bool
	media    string       // tmdb.MediaMovie | tmdb.MediaTV
	selected map[int]bool // genre id -> chosen, for the current media type
	shown    bool
}

// genreAction is what a key press did to the picker, from the model's point of
// view. Media switches and toggles are handled internally (genreIdle); only
// closing the modal needs the model to react.
type genreAction int

const (
	genreIdle      genreAction = iota // still open; nothing for the model to do
	genreCancelled                    // closed without applying
	genreApplied                      // closed; run a discover query
)

func newGenrePicker(st styles, h help.Model) genrePicker {
	l := list.New(nil, newGenreDelegate(), 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(true)
	l.SetFilteringEnabled(false) // bare letters drive picker commands
	// Bare keys are forwarded to this list, whose default keymap quits the
	// program on "q". Cancel is handled upstream, so disable its quit bindings.
	// SetEnabled alone is reverted by the list's updateKeybindings on every
	// state change; DisableQuitKeybindings sets the flag that survives.
	l.DisableQuitKeybindings()

	return genrePicker{
		list:     l,
		styles:   st,
		help:     h,
		media:    tmdb.MediaMovie,
		selected: map[int]bool{},
	}
}

// setCatalog installs the loaded genre lists, refreshing the checklist if the
// modal is already open (the user hit the key before the fetch returned).
func (g *genrePicker) setCatalog(cat tmdb.GenreCatalog) {
	g.catalog = cat
	g.loaded = true
	if g.shown {
		g.reload()
	}
}

// open reveals the modal. It populates itself once the catalog loads.
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

// update handles one key while the modal is open, mutating the draft and
// reporting whether the modal closed.
func (g *genrePicker) update(msg tea.KeyMsg, keys keyMap, w, h int) (tea.Cmd, genreAction) {
	switch {
	case key.Matches(msg, keys.Back):
		g.shown = false
		return nil, genreCancelled
	case key.Matches(msg, keys.Enter):
		g.shown = false
		return nil, genreApplied
	case key.Matches(msg, keys.Space):
		if it, ok := g.list.SelectedItem().(genreItem); ok {
			g.selected[it.g.ID] = !g.selected[it.g.ID]
			g.reload()
		}
		return nil, genreIdle
	case key.Matches(msg, keys.ClearGenres):
		clear(g.selected)
		g.reload()
		return nil, genreIdle
	case key.Matches(msg, keys.OverlayMedia):
		g.toggleMedia(w, h)
		return nil, genreIdle
	}
	var cmd tea.Cmd
	g.list, cmd = g.list.Update(msg)
	return cmd, genreIdle
}

// toggleMedia flips movie⇄TV. Genre ids are namespaced per media type, so the
// draft selection is cleared and the checklist swapped.
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

// reload rebuilds the checklist items from the catalog + draft, preserving the
// cursor. Called after any change to selection or media.
func (g *genrePicker) reload() {
	idx := g.list.Index()
	genres := g.catalog.For(g.media)
	items := make([]list.Item, len(genres))
	for i, gr := range genres {
		items[i] = genreItem{g: gr, selected: g.selected[gr.ID]}
	}
	g.list.SetItems(items)
	g.list.Select(idx)
}

// mediaType is the media the draft targets (tmdb.MediaMovie | tmdb.MediaTV).
func (g genrePicker) mediaType() string { return g.media }

// genreIDs returns the selected genre ids in a stable order.
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

// selectedNames returns the selected genres' display names, catalogue order.
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

// view renders the modal centered on a w×h screen. spin animates the loading
// state before the catalog arrives.
func (g genrePicker) view(spin spinner.Model, w, h int) string {
	title := lipgloss.JoinHorizontal(lipgloss.Left,
		g.styles.TitleText.Render("Filter by genre"),
		g.styles.Muted.Render("  ·  "+g.mediaLabel()),
	)

	body := g.styles.Muted.Render(spin.View() + " loading genres…")
	if g.loaded {
		body = g.list.View()
	}

	count := "no genres selected"
	if n := len(g.genreIDs()); n > 0 {
		count = fmt.Sprintf("%d selected", n)
	}

	hints := g.help.Styles.ShortDesc.Render(
		"space toggle · ↵ apply · m movies/tv · c clear · esc cancel",
	)

	content := lipgloss.JoinVertical(lipgloss.Left,
		title, "", body, "",
		g.styles.Muted.Render(count), hints,
	)
	box := lipgloss.NewStyle().
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Render(content)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

// ---------------------------------------------------------------- list item

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

// newGenreDelegate is a compact single-line delegate for the checklist.
func newGenreDelegate() list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetSpacing(0)
	d.Styles.NormalTitle = d.Styles.NormalTitle.Foreground(fgPrimary)
	d.Styles.SelectedTitle = d.Styles.SelectedTitle.Foreground(accent).BorderForeground(accent).Bold(true)
	d.Styles.DimmedTitle = d.Styles.DimmedTitle.Foreground(fgMuted)
	return d
}

// ---------------------------------------------------------------- messages + cmds

type genresLoadedMsg struct {
	cat tmdb.GenreCatalog
	err error
}

func loadGenresCmd(ctx context.Context, c *tmdb.Client) tea.Cmd {
	return func() tea.Msg {
		cat, err := c.Genres(ctx)
		return genresLoadedMsg{cat: cat, err: err}
	}
}
