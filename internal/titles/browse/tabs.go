package browse

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/stupside/castor/internal/titles/tmdb"
)

type tabID int

const (
	tabTrending tabID = iota
	tabPopularMovies
	tabTopMovies
	tabPopularTV
	tabTopTV
	tabCount
)

func (t tabID) label() string {
	return [...]string{"Trending", "Popular Movies", "Top Movies", "Popular TV", "Top TV"}[t]
}

// fetch uses discover, not TMDB's popular/top_rated lists, so unreleased titles stay out.
func (t tabID) fetch(ctx context.Context, c Catalog) ([]tmdb.SearchResult, error) {
	var p tmdb.DiscoverParams
	switch t {
	case tabPopularMovies:
		p = tmdb.DiscoverParams{MediaType: tmdb.MediaMovie, Sort: tmdb.SortPopularity}
	case tabTopMovies:
		p = tmdb.DiscoverParams{MediaType: tmdb.MediaMovie, Sort: tmdb.SortRating}
	case tabPopularTV:
		p = tmdb.DiscoverParams{MediaType: tmdb.MediaTV, Sort: tmdb.SortPopularity}
	case tabTopTV:
		p = tmdb.DiscoverParams{MediaType: tmdb.MediaTV, Sort: tmdb.SortRating}
	default:
		return c.Trending(ctx)
	}
	pg, err := c.Discover(ctx, p)
	return pg.Results, err
}

func (m *model) cycleTab(delta int) tea.Cmd {
	m.topsCursor[m.tab] = m.results.Index()
	n := int(tabCount)
	m.tab = tabID(((int(m.tab)+delta)%n + n) % n)
	if m.mode != modeCurated {
		m.mode = modeCurated
		m.resize()
	}
	return m.ensureTabLoaded()
}

func (m *model) applyTab() {
	m.results.SetItems(m.topsCache[m.tab])
	if m.topsCursor[m.tab] < len(m.topsCache[m.tab]) {
		m.results.Select(m.topsCursor[m.tab])
	}
}

// applyMode restores feed after query clears; spinner/error were abandoned.
func (m *model) applyMode() {
	m.loading = false
	m.err = nil
	if m.mode == modeDiscover {
		m.results.SetItems(toResultItems(m.disc.results))
		m.results.Select(0)
		return
	}
	m.applyTab()
}

func (m *model) ensureTabLoaded() tea.Cmd {
	if m.topsCache[m.tab] != nil {
		m.applyTab()
		return m.inspector.hover()
	}
	m.loading = true
	m.err = nil
	return tea.Batch(loadTopCmd(m.ctx, m.client, m.tab), m.spin.Tick)
}
