package browse

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/stupside/castor/internal/browse/tmdb"
)

type drillMode int

const (
	modeSeasons drillMode = iota
	modeEpisodes
)

// drilldown is the TV navigation screen: seasons, then episodes. It owns the
// show identity, the list, and which level is showing. The model asks it to
// begin (from a picked TV result) and feeds it the async season/episode
// payloads; the drilldown decides what each key press means and hands back an
// outcome.
type drilldown struct {
	ctx    context.Context
	client *tmdb.Client

	list         list.Model
	mode         drillMode
	tvID         int
	tvName       string
	seasonNum    int
	seasonsCache []list.Item // restore target for episodes → seasons back
}

func newDrilldown(ctx context.Context, client *tmdb.Client, delegate list.DefaultDelegate) drilldown {
	l := list.New(nil, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	return drilldown{ctx: ctx, client: client, list: l}
}

// drillOutcome is what a key press produced, from the model's point of view.
type drillOutcome struct {
	cmd      tea.Cmd    // a command to run (season fetch, or list housekeeping)
	loading  bool       // cmd is a network fetch → show the spinner
	exit     bool       // leave the drilldown, back to browse
	selected *Selection // an episode was chosen → quit with this
}

// begin loads the seasons for a picked TV show. The model switches screens once
// showSeasons runs on the returned data.
func (d *drilldown) begin(id int, name string) tea.Cmd {
	d.tvID = id
	d.tvName = name
	return tvCmd(d.ctx, d.client, id)
}

func (d *drilldown) setSize(w, h int) { d.list.SetSize(max(w, 30), h) }

func (d drilldown) filtering() bool { return d.list.SettingFilter() }

// showSeasons installs the season list from fetched show details.
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

// showEpisodes installs the episode list for the selected season.
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

// update handles one message while the drilldown is showing.
func (d *drilldown) update(msg tea.Msg, keys keyMap) drillOutcome {
	if km, ok := msg.(tea.KeyMsg); ok && !d.list.SettingFilter() {
		switch {
		case key.Matches(km, keys.Back):
			switch d.mode {
			case modeEpisodes:
				d.list.SetItems(d.seasonsCache)
				d.list.Select(0)
				d.mode = modeSeasons
				return drillOutcome{}
			case modeSeasons:
				return drillOutcome{exit: true}
			}
		case key.Matches(km, keys.Enter):
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

// view renders the breadcrumb header and the list body. The model appends the
// shared footer.
func (d drilldown) view(st styles) string {
	sep := st.Muted.Render(" › ")
	parts := []string{st.TitleText.Render(d.tvName)}
	switch d.mode {
	case modeSeasons:
		parts = append(parts, sep, st.TitleText.Render("Seasons"))
	case modeEpisodes:
		parts = append(parts,
			sep, st.TitleText.Render(fmt.Sprintf("S%02d", d.seasonNum)),
			sep, st.TitleText.Render("Episodes"),
		)
	}
	header := headerPad(strings.Join(parts, ""))
	return lipgloss.JoinVertical(lipgloss.Left, header, "", d.list.View())
}

// ---------------------------------------------------------------- list items

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

// ---------------------------------------------------------------- messages + cmds

type tvDoneMsg struct {
	tv  *tmdb.TVDetails
	err error
}

type seasonDoneMsg struct {
	sd  *tmdb.SeasonDetails
	err error
}

func tvCmd(ctx context.Context, c *tmdb.Client, id int) tea.Cmd {
	return func() tea.Msg {
		tv, err := c.TV(ctx, id)
		return tvDoneMsg{tv: tv, err: err}
	}
}

func seasonCmd(ctx context.Context, c *tmdb.Client, tvID, n int) tea.Cmd {
	return func() tea.Msg {
		sd, err := c.Season(ctx, tvID, n)
		return seasonDoneMsg{sd: sd, err: err}
	}
}
