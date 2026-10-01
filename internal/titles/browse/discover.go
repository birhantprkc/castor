package browse

import (
	"context"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/stupside/castor/internal/titles/tmdb"
)

// browseMode is the source of the results list on screenBrowse: non-empty search overrides both modes.
type browseMode int

const (
	modeCurated browseMode = iota
	modeDiscover
)

// sorts is the order ^s cycles through.
var sorts = []tmdb.Sort{tmdb.SortPopularity, tmdb.SortRating, tmdb.SortNewest}

func sortLabel(s tmdb.Sort) string {
	switch s {
	case tmdb.SortRating:
		return "Rating"
	case tmdb.SortNewest:
		return "Newest"
	default:
		return "Popularity"
	}
}

// discoverState holds the sort, accumulated pages, token that invalidates in-flight pages on filter change.
type discoverState struct {
	sort        tmdb.Sort
	results     []tmdb.SearchResult
	page        int
	hasMore     bool
	loadingMore bool
	tok         int
}

type discoverDoneMsg struct {
	tok        int
	page       int
	res        []tmdb.SearchResult
	totalPages int
	err        error
}

func discoverCmd(ctx context.Context, c Catalog, tok int, p tmdb.DiscoverParams) tea.Cmd {
	return func() tea.Msg {
		pg, err := c.Discover(ctx, p)
		return discoverDoneMsg{tok: tok, page: p.Page, res: pg.Results, totalPages: pg.TotalPages, err: err}
	}
}

// discParams snapshots the picker's filter and the current sort as a query.
func (m model) discParams(page int) tmdb.DiscoverParams {
	return tmdb.DiscoverParams{
		MediaType: m.picker.media,
		GenreIDs:  m.picker.genreIDs(),
		Sort:      m.disc.sort,
		Page:      page,
	}
}

func (m *model) enterDiscover() tea.Cmd {
	m.mode = modeDiscover
	m.query.SetValue("")
	m.queryTok++
	m.disc.results = nil
	m.disc.page = 1
	m.disc.hasMore = false
	m.disc.loadingMore = false
	m.disc.tok++
	m.loading = true
	m.err = nil
	m.resize()
	return tea.Batch(discoverCmd(m.ctx, m.client, m.disc.tok, m.discParams(1)), m.spin.Tick)
}

func (m *model) exitDiscover() tea.Cmd {
	m.mode = modeCurated
	m.resize()
	return m.ensureTabLoaded()
}

// cycleSort advances the sort and refetches from page one.
func (m *model) cycleSort() tea.Cmd {
	i := slices.Index(sorts, m.disc.sort)
	m.disc.sort = sorts[(i+1)%len(sorts)]
	return m.enterDiscover()
}

func (m *model) onDiscoverDone(msg discoverDoneMsg) tea.Cmd {
	if msg.tok != m.disc.tok {
		return nil // stale: filters changed while this was in flight
	}
	m.loading = false
	m.disc.loadingMore = false
	if msg.err != nil {
		m.err = msg.err
		return nil
	}
	m.disc.page = msg.page
	m.disc.hasMore = msg.page < msg.totalPages
	if msg.page <= 1 {
		m.disc.results = msg.res
		m.results.SetItems(toResultItems(msg.res))
		m.results.Select(0)
	} else {
		m.disc.results = append(m.disc.results, msg.res...)
		m.results.SetItems(toResultItems(m.disc.results))
	}
	return m.inspector.hover()
}

func (m *model) maybeLoadMore() tea.Cmd {
	if m.mode != modeDiscover || m.query.Value() != "" {
		return nil
	}
	if m.disc.loadingMore || !m.disc.hasMore {
		return nil
	}
	if m.results.Index() < len(m.disc.results)-3 {
		return nil
	}
	m.disc.loadingMore = true
	return discoverCmd(m.ctx, m.client, m.disc.tok, m.discParams(m.disc.page+1))
}
