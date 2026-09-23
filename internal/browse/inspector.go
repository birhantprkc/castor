package browse

import (
	"context"
	"fmt"
	"image/color"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/eliukblau/pixterm/pkg/ansimage"

	"github.com/stupside/castor/internal/browse/tmdb"
)

// hoverDebounce collapses cursor movement bursts to avoid requests per row.
const hoverDebounce = 120 * time.Millisecond

// inspector manages the right-hand panel with lazy poster and metadata caches.
type inspector struct {
	ctx    context.Context
	client *tmdb.Client
	styles styles

	posters       map[string]string        // posterPath -> rendered ANSI
	details       map[string]*tmdb.Details // detailKey -> details
	posterPending string
	detailPending map[string]bool
	tok           int // debounce token; only the latest hover settles
}

func newInspector(ctx context.Context, client *tmdb.Client, st styles) inspector {
	return inspector{
		ctx:           ctx,
		client:        client,
		styles:        st,
		posters:       map[string]string{},
		details:       map[string]*tmdb.Details{},
		detailPending: map[string]bool{},
	}
}

// hover is called on every selection change; only the final tick survives the token check.
func (in *inspector) hover() tea.Cmd {
	in.tok++
	return hoverSettleCmd(in.tok)
}

// update consumes async messages for settled hovers and ready assets.
func (in *inspector) update(msg tea.Msg, sel *tmdb.SearchResult) tea.Cmd {
	switch msg := msg.(type) {
	case hoverSettleMsg:
		if msg.tok != in.tok || sel == nil {
			return nil
		}
		return in.load(*sel)
	case posterReadyMsg:
		if msg.err == nil && msg.ansi != "" {
			in.posters[msg.posterPath] = msg.ansi
		}
		if msg.posterPath == in.posterPending {
			in.posterPending = ""
		}
		return nil
	case detailsReadyMsg:
		delete(in.detailPending, msg.key)
		if msg.err == nil {
			in.details[msg.key] = msg.d
		}
		return nil
	}
	return nil
}

// load is deduped against both the cache and in-flight requests.
func (in *inspector) load(r tmdb.SearchResult) tea.Cmd {
	return tea.Batch(in.loadPoster(r), in.loadDetails(r))
}

func (in *inspector) loadPoster(r tmdb.SearchResult) tea.Cmd {
	if r.PosterPath == "" || in.posterPending == r.PosterPath {
		return nil
	}
	if _, ok := in.posters[r.PosterPath]; ok {
		return nil
	}
	in.posterPending = r.PosterPath
	return fetchPosterCmd(in.ctx, in.client, r.PosterPath, posterCols, posterRows)
}

func (in *inspector) loadDetails(r tmdb.SearchResult) tea.Cmd {
	key := detailKey(r.MediaType, r.ID)
	if _, done := in.details[key]; done {
		return nil
	}
	if in.detailPending[key] {
		return nil
	}
	in.detailPending[key] = true
	return detailsCmd(in.ctx, in.client, r.MediaType, r.ID)
}

// view renders the column, clamped to height rows; lipgloss rewrites ANSI, so poster strings bypass it.
func (in inspector) view(sel *tmdb.SearchResult, height int) string {
	if sel == nil {
		return blankRect(posterCols, height)
	}
	r := *sel

	poster := blankRect(posterCols, posterRows)
	if ansi, ok := in.posters[r.PosterPath]; ok && r.PosterPath != "" {
		poster = ansi
	}

	title := r.DisplayTitle()
	if y := r.Year(); y != "" {
		title = fmt.Sprintf("%s (%s)", title, y)
	}
	title = truncate(title, posterCols)

	d := in.details[detailKey(r.MediaType, r.ID)]
	info, tagline, cast := metaLines(r, d, posterCols)

	// Poster fixed; overview flexes into remaining vertical space.
	meta := []string{"", in.styles.MetaTitle.Render(title)}
	if info != "" {
		meta = append(meta, in.styles.Muted.Render(info))
	}
	if tagline != "" {
		meta = append(meta, in.styles.Tagline.Render(tagline))
	}
	meta = append(meta, "") // spacer before the overview

	castLines := 0
	if cast != "" {
		castLines = 1
	}
	overviewH := max(height-posterRows-len(meta)-castLines, 0)
	if overviewH > 0 {
		meta = append(meta, in.styles.Overview.MaxHeight(overviewH).Render(r.Overview))
	}
	if cast != "" {
		meta = append(meta, in.styles.Muted.Render(cast))
	}
	return clampRows(poster+"\n"+strings.Join(meta, "\n"), height)
}

type posterReadyMsg struct {
	posterPath string
	ansi       string
	err        error
}

type detailsReadyMsg struct {
	key string
	d   *tmdb.Details
	err error
}

type hoverSettleMsg struct{ tok int }

func hoverSettleCmd(tok int) tea.Cmd {
	return tea.Tick(hoverDebounce, func(time.Time) tea.Msg { return hoverSettleMsg{tok: tok} })
}

func detailsCmd(ctx context.Context, c *tmdb.Client, mediaType string, id int) tea.Cmd {
	key := detailKey(mediaType, id)
	return func() tea.Msg {
		d, err := c.Details(ctx, mediaType, id)
		return detailsReadyMsg{key: key, d: d, err: err}
	}
}

func detailKey(mediaType string, id int) string { return mediaType + ":" + strconv.Itoa(id) }

// fetchPosterCmd renders the poster to ANSI escapes; half-blocks show 2 pixels per cell.
func fetchPosterCmd(ctx context.Context, c *tmdb.Client, posterPath string, cols, rows int) tea.Cmd {
	return func() tea.Msg {
		body, err := c.Poster(ctx, posterPath, "w500")
		if err != nil {
			return posterReadyMsg{posterPath: posterPath, err: err}
		}
		defer func() { _ = body.Close() }()

		// NoDithering uses true-color; ScaleModeResize fits exactly to prevent layout shift.
		img, err := ansimage.NewScaledFromReader(
			body,
			rows*2, cols,
			color.Transparent,
			ansimage.ScaleModeResize,
			ansimage.NoDithering,
		)
		if err != nil {
			return posterReadyMsg{posterPath: posterPath, err: err}
		}
		return posterReadyMsg{posterPath: posterPath, ansi: img.Render()}
	}
}

// metaLines clamps each line; rating from list, runtime/genres/cast from details.
func metaLines(r tmdb.SearchResult, d *tmdb.Details, width int) (info, tagline, cast string) {
	var parts []string
	if r.VoteAverage > 0 {
		parts = append(parts, fmt.Sprintf("★ %.1f", r.VoteAverage))
	}
	if d != nil {
		if rt := formatRuntime(d.RuntimeMinutes()); rt != "" {
			parts = append(parts, rt)
		}
		if names := d.GenreNames(); len(names) > 0 {
			parts = append(parts, strings.Join(names, ", "))
		}
	}
	info = truncate(strings.Join(parts, " · "), width)

	if d != nil {
		tagline = truncate(d.Tagline, width)
		if c := d.TopCast(3); len(c) > 0 {
			cast = truncate("With "+strings.Join(c, ", "), width)
		}
	}
	return info, tagline, cast
}

func formatRuntime(minutes int) string {
	if minutes <= 0 {
		return ""
	}
	h, m := minutes/60, minutes%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}

func blankRect(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	return strings.Join(slices.Repeat([]string{strings.Repeat(" ", w)}, h), "\n")
}

// clampRows forces s to exactly n lines; truncate or pad with blanks.
func clampRows(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	// n-len(lines) cannot go negative: the truncation above leaves at most n lines.
	lines = append(lines, make([]string, n-len(lines))...)
	return strings.Join(lines, "\n")
}
