// Package browse asks the operator which device to cast to and which title to cast, browsing TMDB for it.
package browse

import (
	"context"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stupside/castor/tui/internal/browse/tmdb"
	"github.com/stupside/castor/tui/internal/palette"
)

type Kind int

const (
	KindMovie Kind = iota
	KindEpisode
)

// Selection is the title the operator chose to cast.
type Selection struct {
	Kind    Kind
	TMDBID  string
	Title   string
	Season  uint
	Episode uint
}

// title runs the browser until a title is chosen, or the operator leaves without one.
func title(ctx context.Context, client Catalog, badge string) (Selection, bool, error) {
	final, err := tea.NewProgram(newModel(ctx, client, badge), tea.WithContext(ctx)).Run()
	if err != nil {
		return Selection{}, false, err
	}
	if fm, ok := final.(model); ok && fm.sel != nil {
		return *fm.sel, true, nil
	}
	return Selection{}, false, nil
}

const (
	// Poster: 27×40 cells approximate 2:3 movie ratio (prevents pixterm horizontal stretch).
	posterCols     = 27
	posterRows     = 20
	searchDebounce = 250 * time.Millisecond
)

type screen int

const (
	screenBrowse screen = iota
	screenDrilldown
)

type model struct {
	ctx    context.Context
	client Catalog

	styles styles
	keys   keyMap
	help   help.Model
	spin   spinner.Model

	scr screen

	tab        tabID
	mode       browseMode
	query      textinput.Model
	queryTok   int // monotonic; only the latest debounce tick fires
	results    list.Model
	topsCache  [tabCount][]list.Item
	topsCursor [tabCount]int
	disc       discoverState

	inspector inspector
	genres    genrePicker
	drill     drilldown

	loading bool
	err     error

	sel  *Selection
	w, h int

	badge string
}

func newModel(ctx context.Context, client Catalog, badge string) model {
	q := textinput.New()
	q.Placeholder = "Type to search TMDB…"
	q.Prompt = "❯ "
	q.CharLimit = 128
	q.Focus()

	results := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	results.SetShowTitle(false)
	results.SetShowStatusBar(false)
	results.SetShowHelp(false)
	results.SetFilteringEnabled(false) // the textinput owns filtering on browse

	m := model{
		ctx:       ctx,
		client:    client,
		keys:      defaultKeys(),
		help:      help.New(),
		spin:      spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		scr:       screenBrowse,
		tab:       tabTrending,
		mode:      modeCurated,
		query:     q,
		results:   results,
		disc:      discoverState{sort: tmdb.SortPopularity},
		inspector: newInspector(ctx, client),
		genres:    newGenrePicker(),
		drill:     newDrilldown(ctx, client),
		loading:   true,
		badge:     badge,
	}
	m.restyle(true)
	return m
}

// restyle repaints for the terminal background; dark until the terminal reports it.
func (m *model) restyle(dark bool) {
	p := palette.New(dark)
	m.styles = newStyles(p)
	m.help.Styles = newHelpStyles(p)
	m.spin.Style = lipgloss.NewStyle().Foreground(p.Accent)
	m.query.SetStyles(newQueryStyles(p))
	m.results.SetDelegate(p.Delegate())
	p.StyleList(&m.results)
	m.inspector.styles = m.styles
	m.genres.restyle(p, m.styles)
	m.drill.restyle(p)
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		tea.RequestBackgroundColor,
		m.spin.Tick,
		textinput.Blink,
		loadTopCmd(m.ctx, m.client, m.tab),
		loadGenresCmd(m.ctx, m.client),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.restyle(msg.IsDark())
		return m, nil

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.help.SetWidth(msg.Width)
		m.resize()
		if m.genres.shown {
			m.genres.resize(m.w, m.h)
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		if key.Matches(msg, m.keys.quit) {
			return m, tea.Quit
		}
		if m.genres.shown {
			cmd, action := m.genres.update(msg, m.keys, m.w, m.h)
			if action == genreApplied {
				cmd = m.enterDiscover()
			}
			return m, cmd
		}
		if key.Matches(msg, m.keys.help) && !m.drilldownFiltering() {
			m.help.ShowAll = !m.help.ShowAll
			m.resize()
			return m, nil
		}

	case genresLoadedMsg:
		if msg.err == nil {
			m.genres.setCatalog(msg.cat, m.w, m.h)
		}
		return m, nil

	case topsLoadedMsg:
		return m.onTopsLoaded(msg)

	case searchTickMsg:
		return m.onSearchTick(msg)

	case searchDoneMsg:
		if msg.tok != m.queryTok {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.results.SetItems(toResultItems(msg.res))
		m.results.Select(0)
		return m, m.inspector.hover()

	case discoverDoneMsg:
		return m, m.onDiscoverDone(msg)

	case posterReadyMsg, detailsReadyMsg, hoverSettleMsg:
		cmd := m.inspector.update(msg, m.selectedResult())
		return m, cmd

	case tvDoneMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.drill.showSeasons(msg.tv)
		m.goTo(screenDrilldown)
		return m, nil

	case seasonDoneMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.drill.showEpisodes(msg.sd)
		return m, nil
	}

	switch m.scr {
	case screenBrowse:
		return m.updateBrowse(msg)
	case screenDrilldown:
		return m.updateDrilldown(msg)
	}
	return m, nil
}

func (m model) updateBrowse(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m.forwardToQuery(msg)
	}
	switch {
	case key.Matches(km, m.keys.genres):
		m.genres.open(m.w, m.h)
		return m, nil
	case key.Matches(km, m.keys.sort):
		if m.mode == modeDiscover {
			return m, m.cycleSort()
		}
		return m, nil
	case key.Matches(km, m.keys.media):
		if m.mode == modeDiscover {
			m.genres.toggleMedia(m.w, m.h)
			return m, m.enterDiscover()
		}
		return m, nil
	case key.Matches(km, m.keys.tab):
		if m.query.Value() == "" {
			return m, m.cycleTab(1)
		}
		return m, nil
	case key.Matches(km, m.keys.shiftTab):
		if m.query.Value() == "" {
			return m, m.cycleTab(-1)
		}
		return m, nil
	case key.Matches(km, m.keys.enter):
		if r := m.selectedResult(); r != nil {
			return m.pickResult(*r)
		}
		return m, nil
	case key.Matches(km, m.keys.back):
		return m.onBrowseBack()
	case key.Matches(km, m.keys.up),
		key.Matches(km, m.keys.down),
		key.Matches(km, m.keys.pageUp),
		key.Matches(km, m.keys.pageDown):
		return m.delegateResults(msg)
	}
	return m.forwardToQuery(msg)
}

func (m model) onBrowseBack() (tea.Model, tea.Cmd) {
	if m.query.Value() != "" {
		m.query.SetValue("")
		m.queryTok++
		m.applyMode()
		m.resize() // the discover filter bar reappears on query clear
		return m, m.inspector.hover()
	}
	if m.mode == modeDiscover {
		return m, m.exitDiscover()
	}
	return m, nil
}

func (m model) updateDrilldown(msg tea.Msg) (tea.Model, tea.Cmd) {
	out := m.drill.update(msg, m.keys)
	switch {
	case out.selected != nil:
		m.sel = out.selected
		return m, tea.Quit
	case out.exit:
		m.goTo(screenBrowse)
		return m, nil
	case out.loading:
		m.loading = true
		m.err = nil
		return m, tea.Batch(out.cmd, m.spin.Tick)
	}
	return m, out.cmd
}

func (m model) drilldownFiltering() bool {
	return m.scr == screenDrilldown && m.drill.list.SettingFilter()
}

// goTo is the only way to change screen; different chrome needs recompute.
func (m *model) goTo(s screen) {
	m.scr = s
	m.resize()
}

func (m *model) resize() {
	h := m.bodyHeight()
	m.results.SetSize(max(m.w-posterCols-spGutter, 30), h)
	m.drill.setSize(m.w, h)
	m.query.SetWidth(max(m.w-spInline*2, 20))
}
