// Package browse is a Bubble Tea TUI for searching TMDB and picking media to cast (results browser).
package browse

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/stupside/castor/internal/browse/tmdb"
	"github.com/stupside/castor/internal/device"
)

type Kind int

const (
	KindNone Kind = iota
	KindMovie
	KindEpisode
)

type Selection struct {
	Kind    Kind
	TMDBID  string
	Title   string
	Season  uint
	Episode uint
}

func Run(ctx context.Context, client *tmdb.Client, devName string, devType device.Type) (Selection, error) {
	final, err := tea.NewProgram(newModel(ctx, client, devName, devType), tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if err != nil {
		return Selection{}, err
	}
	if fm, ok := final.(model); ok {
		return fm.sel, nil
	}
	return Selection{}, nil
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
func (t tabID) fetch(ctx context.Context, c *tmdb.Client) ([]tmdb.SearchResult, error) {
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

type resultItem struct{ r tmdb.SearchResult }

func (i resultItem) Title() string {
	if y := i.r.Year(); y != "" {
		return fmt.Sprintf("%s (%s)", i.r.DisplayTitle(), y)
	}
	return i.r.DisplayTitle()
}

func (i resultItem) Description() string {
	typ := "Movie"
	if i.r.MediaType == tmdb.MediaTV {
		typ = "TV"
	}
	if i.r.Overview == "" {
		return typ
	}
	return typ + " · " + truncate(i.r.Overview, 80)
}

func (i resultItem) FilterValue() string { return i.r.DisplayTitle() }

func toResultItems(rs []tmdb.SearchResult) []list.Item {
	items := make([]list.Item, len(rs))
	for i, r := range rs {
		items[i] = resultItem{r: r}
	}
	return items
}

type model struct {
	ctx    context.Context
	client *tmdb.Client

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
	topsLoaded [tabCount]bool
	topsCursor [tabCount]int
	disc       discoverState

	inspector inspector
	picker    genrePicker
	drill     drilldown

	loading bool
	err     error

	sel  Selection
	w, h int

	devBadge string
}

func newModel(ctx context.Context, client *tmdb.Client, devName string, devType device.Type) model {
	st := newStyles()
	hlp := newHelp()

	q := textinput.New()
	q.Placeholder = "Type to search TMDB…"
	q.Prompt = "❯ "
	q.PromptStyle = lipgloss.NewStyle().Foreground(accent)
	q.PlaceholderStyle = lipgloss.NewStyle().Foreground(fgMuted)
	q.TextStyle = lipgloss.NewStyle().Foreground(fgPrimary)
	q.CharLimit = 128
	q.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(accent)

	delegate := newDelegate()

	results := list.New(nil, delegate, 0, 0)
	results.SetShowTitle(false)
	results.SetShowStatusBar(false)
	results.SetShowHelp(false)
	results.SetFilteringEnabled(false) // the textinput owns filtering on browse

	return model{
		ctx:       ctx,
		client:    client,
		styles:    st,
		keys:      defaultKeys(),
		help:      hlp,
		spin:      sp,
		scr:       screenBrowse,
		tab:       tabTrending,
		mode:      modeCurated,
		query:     q,
		results:   results,
		disc:      discoverState{sort: tmdb.SortPopularity},
		inspector: newInspector(ctx, client, st),
		picker:    newGenrePicker(st, hlp),
		drill:     newDrilldown(ctx, client, delegate),
		loading:   true,
		devBadge:  strings.ToUpper(string(devType)) + "  " + devName,
	}
}

func newDelegate() list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.Styles.NormalTitle = d.Styles.NormalTitle.Foreground(fgPrimary)
	d.Styles.NormalDesc = d.Styles.NormalDesc.Foreground(fgMuted)
	d.Styles.SelectedTitle = d.Styles.SelectedTitle.Foreground(accent).BorderForeground(accent).Bold(true)
	d.Styles.SelectedDesc = d.Styles.SelectedDesc.Foreground(fgSecondary).BorderForeground(accent)
	d.Styles.DimmedTitle = d.Styles.DimmedTitle.Foreground(fgMuted)
	d.Styles.DimmedDesc = d.Styles.DimmedDesc.Foreground(fgMuted)
	d.Styles.FilterMatch = lipgloss.NewStyle().Foreground(accent).Underline(true)
	return d
}

func newHelp() help.Model {
	h := help.New()
	h.ShowAll = false
	h.Styles.ShortKey = h.Styles.ShortKey.Foreground(fgSecondary)
	h.Styles.ShortDesc = h.Styles.ShortDesc.Foreground(fgMuted)
	h.Styles.ShortSeparator = h.Styles.ShortSeparator.Foreground(fgMuted)
	h.Styles.FullKey = h.Styles.FullKey.Foreground(fgSecondary)
	h.Styles.FullDesc = h.Styles.FullDesc.Foreground(fgMuted)
	h.Styles.FullSeparator = h.Styles.FullSeparator.Foreground(fgMuted)
	h.Styles.Ellipsis = h.Styles.Ellipsis.Foreground(fgMuted)
	return h
}

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

func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.spin.Tick,
		textinput.Blink,
		loadTopCmd(m.ctx, m.client, m.tab),
		loadGenresCmd(m.ctx, m.client),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.help.Width = msg.Width
		m.resize()
		if m.picker.shown {
			m.picker.resize(m.w, m.h)
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		if key.Matches(msg, m.keys.Quit) {
			return m, tea.Quit
		}
		if m.picker.shown {
			cmd, action := m.picker.update(msg, m.keys, m.w, m.h)
			if action == genreApplied {
				cmd = m.enterDiscover()
			}
			return m, cmd
		}
		if key.Matches(msg, m.keys.Help) && !m.drilldownFiltering() {
			m.help.ShowAll = !m.help.ShowAll
			m.resize()
			return m, nil
		}

	case genresLoadedMsg:
		if msg.err == nil {
			m.picker.setCatalog(msg.cat, m.w, m.h)
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

func (m model) updateBrowse(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m.forwardToQuery(msg)
	}
	switch {
	case key.Matches(km, m.keys.Genres):
		m.picker.open(m.w, m.h)
		return m, nil
	case key.Matches(km, m.keys.Sort):
		if m.mode == modeDiscover {
			return m, m.cycleSort()
		}
		return m, nil
	case key.Matches(km, m.keys.Media):
		if m.mode == modeDiscover {
			m.picker.toggleMedia(m.w, m.h)
			return m, m.enterDiscover()
		}
		return m, nil
	case key.Matches(km, m.keys.Tab):
		if m.query.Value() == "" {
			return m, m.cycleTab(1)
		}
		return m, nil
	case key.Matches(km, m.keys.ShiftTab):
		if m.query.Value() == "" {
			return m, m.cycleTab(-1)
		}
		return m, nil
	case key.Matches(km, m.keys.Enter):
		if r := m.selectedResult(); r != nil {
			return m.pickResult(*r)
		}
		return m, nil
	case key.Matches(km, m.keys.Back):
		return m.onBrowseBack()
	case key.Matches(km, m.keys.Up),
		key.Matches(km, m.keys.Down),
		key.Matches(km, m.keys.PageUp),
		key.Matches(km, m.keys.PageDown):
		return m.delegateResults(msg)
	}
	return m.forwardToQuery(msg)
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
	if m.topsLoaded[m.tab] {
		m.applyTab()
		return m.inspector.hover()
	}
	m.loading = true
	m.err = nil
	return tea.Batch(loadTopCmd(m.ctx, m.client, m.tab), m.spin.Tick)
}

func (m model) delegateResults(msg tea.Msg) (tea.Model, tea.Cmd) {
	prev := m.results.Index()
	var cmd tea.Cmd
	m.results, cmd = m.results.Update(msg)
	cmds := []tea.Cmd{cmd}
	if m.results.Index() != prev {
		cmds = append(cmds, m.inspector.hover(), m.maybeLoadMore())
	}
	return m, tea.Batch(cmds...)
}

func (m model) pickResult(r tmdb.SearchResult) (tea.Model, tea.Cmd) {
	switch r.MediaType {
	case tmdb.MediaMovie:
		m.sel = Selection{Kind: KindMovie, TMDBID: strconv.Itoa(r.ID), Title: r.DisplayTitle()}
		return m, tea.Quit
	case tmdb.MediaTV:
		m.loading = true
		m.err = nil
		return m, tea.Batch(m.drill.begin(r.ID, r.DisplayTitle()), m.spin.Tick)
	}
	return m, nil
}

func (m model) selectedResult() *tmdb.SearchResult {
	if it, ok := m.results.SelectedItem().(resultItem); ok {
		return &it.r
	}
	return nil
}

func (m model) updateDrilldown(msg tea.Msg) (tea.Model, tea.Cmd) {
	out := m.drill.update(msg, m.keys)
	switch {
	case out.selected != nil:
		m.sel = *out.selected
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
	return m.scr == screenDrilldown && m.drill.filtering()
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
	m.query.Width = max(m.w-spInline*2, 20)
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

func (m model) View() string {
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
		rhs = m.styles.Muted.Render(m.picker.mediaLabel())
	default:
		label = m.tab.label()
		rhs = m.renderTabDots()
	}
	if rhs != "" {
		rhs += "  "
	}
	rhs += m.styles.Muted.Render(m.devBadge)
	title := m.styles.Title.Render(label)
	placed := lipgloss.PlaceHorizontal(max(m.w-lipgloss.Width(title), 0), lipgloss.Right, rhs)
	return lipgloss.JoinHorizontal(lipgloss.Top, title, placed)
}

func (m model) filterBar() string {
	summary := "All genres"
	if names := m.picker.selectedNames(); len(names) > 0 {
		summary = strings.Join(names, ", ")
	}
	left := lipgloss.NewStyle().Padding(0, spInline).Render(
		m.styles.MetaTitle.Render(truncate(summary, max(m.w/2, 12))) +
			m.styles.Muted.Render("  ·  Sort: "+sortLabel(m.disc.sort)),
	)
	hints := m.help.Styles.ShortDesc.Render("^g genres · ^s sort · ^t movie/tv")
	rhs := lipgloss.PlaceHorizontal(max(m.w-lipgloss.Width(left), 0), lipgloss.Right, hints)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, rhs)
}

func (m model) renderTabDots() string {
	active := lipgloss.NewStyle().Foreground(accent)
	inactive := lipgloss.NewStyle().Foreground(fgMuted)
	parts := make([]string, tabCount)
	for i := range tabCount {
		if i == m.tab {
			parts[i] = active.Render("●")
		} else {
			parts[i] = inactive.Render("○")
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
		return pad.Render(m.spin.View() + " " + m.styles.Muted.Render("loading…"))
	case m.err != nil:
		return m.styles.Err.Render("error: " + m.err.Error())
	}
	return ""
}
