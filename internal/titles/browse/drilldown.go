package browse

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stupside/castor/internal/palette"
	"github.com/stupside/castor/internal/titles/tmdb"
)

type drillMode int

const (
	modeSeasons drillMode = iota
	modeEpisodes
)

// drilldown is a TV navigation screen (seasons then episodes) interpreting key presses.
type drilldown struct {
	ctx    context.Context
	client Catalog

	list         list.Model
	mode         drillMode
	tvID         int
	tvName       string
	seasonNum    int
	seasonsCache []list.Item // restore target for episodes → seasons back
}

func newDrilldown(ctx context.Context, client Catalog) drilldown {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	return drilldown{ctx: ctx, client: client, list: l}
}

type drillOutcome struct {
	cmd      tea.Cmd
	loading  bool // show spinner while cmd runs
	exit     bool
	selected *Selection
}

// begin loads seasons for a picked show; model updates display via showSeasons.
func (d *drilldown) begin(id int, name string) tea.Cmd {
	d.tvID = id
	d.tvName = name
	return tvCmd(d.ctx, d.client, id)
}

func (d *drilldown) restyle(p palette.Palette) {
	d.list.SetDelegate(p.Delegate())
	p.StyleList(&d.list)
}

func (d *drilldown) setSize(w, h int) { d.list.SetSize(max(w, 30), h) }

func (d *drilldown) showSeasons(tv *tmdb.TVDetails) {
	d.tvName = tv.Name
	items := make([]list.Item, 0, len(tv.Seasons))
	for _, s := range tv.Seasons {
		if s.EpisodeCount == 0 || s.Unaired() {
			continue // specials, and seasons announced but not yet airing
		}
		items = append(items, seasonItem{s: s})
	}
	d.seasonsCache = items
	d.list.SetItems(items)
	d.list.Select(0)
	d.mode = modeSeasons
}

func (d *drilldown) showEpisodes(sd *tmdb.SeasonDetails) {
	items := make([]list.Item, 0, len(sd.Episodes))
	for _, e := range sd.Episodes {
		if e.Unaired() {
			continue // a season part way through its run lists what is still to come
		}
		items = append(items, episodeItem{e: e})
	}
	d.list.SetItems(items)
	d.list.Select(0)
	d.mode = modeEpisodes
}

func (d *drilldown) update(msg tea.Msg, keys keyMap) drillOutcome {
	if km, ok := msg.(tea.KeyPressMsg); ok && !d.list.SettingFilter() {
		switch {
		case key.Matches(km, keys.back):
			switch d.mode {
			case modeEpisodes:
				d.list.SetItems(d.seasonsCache)
				d.list.Select(0)
				d.mode = modeSeasons
				return drillOutcome{}
			case modeSeasons:
				return drillOutcome{exit: true}
			}
		case key.Matches(km, keys.enter):
			return d.enter()
		}
	}
	var cmd tea.Cmd
	d.list, cmd = d.list.Update(msg)
	return drillOutcome{cmd: cmd}
}

func (d *drilldown) enter() drillOutcome {
	switch d.mode {
	case modeSeasons:
		if it, ok := d.list.SelectedItem().(seasonItem); ok {
			d.seasonNum = it.s.SeasonNumber
			return drillOutcome{cmd: seasonCmd(d.ctx, d.client, d.tvID, it.s.SeasonNumber), loading: true}
		}
	case modeEpisodes:
		if it, ok := d.list.SelectedItem().(episodeItem); ok {
			sel := Selection{
				Kind:    KindEpisode,
				TMDBID:  strconv.Itoa(d.tvID),
				Title:   fmt.Sprintf("%s · S%02dE%02d · %s", d.tvName, d.seasonNum, it.e.EpisodeNumber, it.e.Name),
				Season:  uint(d.seasonNum),
				Episode: uint(it.e.EpisodeNumber),
			}
			return drillOutcome{selected: &sel}
		}
	}
	return drillOutcome{}
}

// view renders breadcrumb header and list body (model appends footer).
func (d drilldown) view(st styles) string {
	sep := st.muted.Render(" › ")
	parts := []string{st.titleText.Render(d.tvName)}
	switch d.mode {
	case modeSeasons:
		parts = append(parts, sep, st.titleText.Render("Seasons"))
	case modeEpisodes:
		parts = append(parts,
			sep, st.titleText.Render(fmt.Sprintf("S%02d", d.seasonNum)),
			sep, st.titleText.Render("Episodes"),
		)
	}
	header := headerPad(strings.Join(parts, ""))
	return lipgloss.JoinVertical(lipgloss.Left, header, "", d.list.View())
}

type seasonItem struct{ s tmdb.Season }

func (i seasonItem) Title() string {
	if i.s.Name != "" {
		return fmt.Sprintf("S%02d · %s", i.s.SeasonNumber, i.s.Name)
	}
	return fmt.Sprintf("Season %d", i.s.SeasonNumber)
}

func (i seasonItem) Description() string {
	return fmt.Sprintf("%d episodes  %s", i.s.EpisodeCount, i.s.AirDate)
}

func (i seasonItem) FilterValue() string { return i.Title() }

type episodeItem struct{ e tmdb.Episode }

func (i episodeItem) Title() string {
	return fmt.Sprintf("E%02d · %s", i.e.EpisodeNumber, i.e.Name)
}

func (i episodeItem) Description() string {
	if i.e.Overview == "" {
		return i.e.AirDate
	}
	return truncate(i.e.Overview, 120)
}

func (i episodeItem) FilterValue() string { return i.e.Name }

type tvDoneMsg struct {
	tv  *tmdb.TVDetails
	err error
}

type seasonDoneMsg struct {
	sd  *tmdb.SeasonDetails
	err error
}

func tvCmd(ctx context.Context, c Catalog, id int) tea.Cmd {
	return func() tea.Msg {
		tv, err := c.TV(ctx, id)
		return tvDoneMsg{tv: tv, err: err}
	}
}

func seasonCmd(ctx context.Context, c Catalog, tvID, n int) tea.Cmd {
	return func() tea.Msg {
		sd, err := c.Season(ctx, tvID, n)
		return seasonDoneMsg{sd: sd, err: err}
	}
}
