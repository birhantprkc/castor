package browse

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stupside/castor/internal/titles/tmdb"
)

type topsLoadedMsg struct {
	tab tabID
	res []tmdb.SearchResult
	err error
}

type searchTickMsg struct {
	tok   int
	query string
}

type searchDoneMsg struct {
	tok int
	res []tmdb.SearchResult
	err error
}

func loadTopCmd(ctx context.Context, c *tmdb.Client, t tabID) tea.Cmd {
	return func() tea.Msg {
		res, err := t.fetch(ctx, c)
		return topsLoadedMsg{tab: t, res: res, err: err}
	}
}

func searchTickCmd(tok int, query string) tea.Cmd {
	return tea.Tick(searchDebounce, func(time.Time) tea.Msg {
		return searchTickMsg{tok: tok, query: query}
	})
}

func searchCmd(ctx context.Context, c *tmdb.Client, tok int, q string) tea.Cmd {
	return func() tea.Msg {
		res, err := c.Search(ctx, q)
		return searchDoneMsg{tok: tok, res: res, err: err}
	}
}

func (m model) onTopsLoaded(msg topsLoadedMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	items := toResultItems(msg.res)
	m.topsCache[msg.tab] = items
	m.topsLoaded[msg.tab] = true
	if m.scr == screenBrowse && m.mode == modeCurated && m.tab == msg.tab && m.query.Value() == "" {
		m.results.SetItems(items)
		m.results.Select(m.topsCursor[msg.tab])
		return m, m.inspector.hover()
	}
	return m, nil
}

func (m model) onSearchTick(msg searchTickMsg) (tea.Model, tea.Cmd) {
	if msg.tok != m.queryTok {
		return m, nil // stale; a newer keystroke supersedes this tick
	}
	if msg.query == "" {
		m.applyMode()
		return m, m.inspector.hover()
	}
	m.loading = true
	m.err = nil
	return m, tea.Batch(searchCmd(m.ctx, m.client, msg.tok, msg.query), m.spin.Tick)
}

// forwardToQuery debounces search and reflows if discover filter appears/disappears.
func (m model) forwardToQuery(msg tea.Msg) (tea.Model, tea.Cmd) {
	prev := m.query.Value()
	var cmd tea.Cmd
	m.query, cmd = m.query.Update(msg)
	if m.query.Value() == prev {
		return m, cmd
	}
	if (prev == "") != (m.query.Value() == "") {
		m.resize()
	}
	m.queryTok++
	return m, tea.Batch(cmd, searchTickCmd(m.queryTok, m.query.Value()))
}
