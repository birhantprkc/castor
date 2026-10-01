package browse

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/stupside/castor/internal/titles/tmdb"
)

func drive(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	tm, cmd := m.Update(msg)
	mm, ok := tm.(model)
	if !ok {
		t.Fatalf("Update returned %T, not model", tm)
	}
	return mm, cmd
}

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func fakeResults(n int) []tmdb.SearchResult {
	rs := make([]tmdb.SearchResult, n)
	for i := range rs {
		rs[i] = tmdb.SearchResult{ID: i + 1, MediaType: tmdb.MediaMovie, Title: fmt.Sprintf("Movie %d", i+1), VoteAverage: 7}
	}
	return rs
}

// The list's default "q" quit binding must not leak out of the overlay.
func TestGenreOverlayDoesNotQuitOnQ(t *testing.T) {
	m := newModel(t.Context(), tmdb.New("dummy"), "", "")
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = drive(t, m, genresLoadedMsg{cat: tmdb.GenreCatalog{
		Movie: []tmdb.Genre{{ID: 28, Name: "Action"}},
	}})
	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlG})

	m, cmd := drive(t, m, runes("q"))
	if !m.picker.shown {
		t.Fatal("q closed the overlay (list quit binding leaked through)")
	}
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("q in overlay quit the program")
		}
	}
}

func TestGenreOverlayOpenedBeforeCatalogIsUsable(t *testing.T) {
	m := newModel(t.Context(), tmdb.New("dummy"), "", "")
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlG})
	m, _ = drive(t, m, genresLoadedMsg{cat: tmdb.GenreCatalog{
		Movie: []tmdb.Genre{{ID: 28, Name: "Action"}, {ID: 35, Name: "Comedy"}},
	}})

	if h := m.picker.list.Height(); h <= 0 {
		t.Fatalf("overlay list height = %d after the catalogue landed", h)
	}
	if !strings.Contains(m.View(), "Action") {
		t.Fatalf("overlay renders no genre rows:\n%s", m.View())
	}
}

func TestGenreCursorClampedOnShorterCatalogue(t *testing.T) {
	m := newModel(t.Context(), tmdb.New("dummy"), "", "")
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = drive(t, m, genresLoadedMsg{cat: tmdb.GenreCatalog{
		Movie: []tmdb.Genre{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}, {ID: 3, Name: "C"}},
		TV:    []tmdb.Genre{{ID: 100, Name: "T"}},
	}})
	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlG})
	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyDown})

	m, _ = drive(t, m, runes("m"))
	if m.picker.list.SelectedItem() == nil {
		t.Fatalf("cursor parked at %d in a %d-item catalogue", m.picker.list.Index(), len(m.picker.list.Items()))
	}
	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if !m.picker.selected[100] {
		t.Fatal("space toggled nothing after the media switch")
	}
}

func TestClearingQueryClearsTransientStatus(t *testing.T) {
	base := newModel(t.Context(), tmdb.New("dummy"), "", "")
	base, _ = drive(t, base, tea.WindowSizeMsg{Width: 100, Height: 30})
	base, _ = drive(t, base, topsLoadedMsg{tab: tabTrending, res: fakeResults(3)})

	search := func(t *testing.T, m model) (model, int) {
		t.Helper()
		m, _ = drive(t, m, runes("a"))
		tok := m.queryTok
		m, _ = drive(t, m, searchTickMsg{tok: tok, query: "a"})
		if !m.loading {
			t.Fatal("the debounce tick should have started the search")
		}
		return m, tok
	}

	m, tok := search(t, base)
	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = drive(t, m, searchDoneMsg{tok: tok, res: fakeResults(9)})
	if m.loading || m.statusLine() != "" {
		t.Fatalf("spinner survived the cleared query: loading=%v status=%q", m.loading, m.statusLine())
	}

	m, tok = search(t, base)
	m, _ = drive(t, m, searchDoneMsg{tok: tok, err: fmt.Errorf("boom")})
	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.err != nil || m.statusLine() != "" {
		t.Fatalf("error survived the cleared query: err=%v status=%q", m.err, m.statusLine())
	}
}

func TestReturningFromDrilldownFitsTheTerminal(t *testing.T) {
	const height = 30
	m := newModel(t.Context(), tmdb.New("dummy"), "", "")
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: height})
	m, _ = drive(t, m, topsLoadedMsg{tab: tabTrending, res: fakeResults(5)})
	m, _ = drive(t, m, tvDoneMsg{tv: &tmdb.TVDetails{
		Name:    "Show",
		Seasons: []tmdb.Season{{SeasonNumber: 1, Name: "One", EpisodeCount: 5, AirDate: "2020-01-01"}},
	}})
	if got := lipgloss.Height(m.View()); got > height {
		t.Fatalf("drilldown renders %d rows in a %d-row terminal", got, height)
	}

	// '?' on the drilldown sizes the shared body for the drilldown's chrome.
	m, _ = drive(t, m, runes("?"))
	m, _ = drive(t, m, runes("?"))
	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.scr != screenBrowse {
		t.Fatal("esc on seasons should return to browse")
	}
	if got := lipgloss.Height(m.View()); got > height {
		t.Fatalf("browse renders %d rows in a %d-row terminal", got, height)
	}
}
