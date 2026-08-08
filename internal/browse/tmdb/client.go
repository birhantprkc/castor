// Package tmdb provides a minimal read-only client for The Movie Database v3 API.
// Only the endpoints needed by the browse subcommand are implemented.
package tmdb

import (
	"bufio"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	defaultBase = "https://api.themoviedb.org/3"
	imageBase   = "https://image.tmdb.org/t/p/"

	// MediaMovie / MediaTV are the two castable TMDB media types. They double
	// as URL path segments (/movie/…, /tv/…) and as the media_type discriminator
	// on mixed result sets.
	MediaMovie = "movie"
	MediaTV    = "tv"
)

// Client is a TMDB v3 API client.
type Client struct {
	apiKey string
	base   string
	http   *http.Client
}

// SearchResult is one row of /search/multi. Movie / TV use different title
// and date fields, so both pairs are exposed and the caller picks based on
// MediaType.
type SearchResult struct {
	ID           int     `json:"id"`
	MediaType    string  `json:"media_type"` // "movie" | "tv" | "person"
	Title        string  `json:"title"`
	Name         string  `json:"name"`
	ReleaseDate  string  `json:"release_date"`
	FirstAirDate string  `json:"first_air_date"`
	Overview     string  `json:"overview"`
	VoteAverage  float64 `json:"vote_average"`
	PosterPath   string  `json:"poster_path"`
}

// PosterURL returns a full URL for the poster at the given TMDB size
// (w92, w154, w185, w342, w500, original). Empty string if no poster.
func (r SearchResult) PosterURL(size string) string {
	if r.PosterPath == "" {
		return ""
	}
	return imageBase + size + r.PosterPath
}

// DisplayTitle returns the user-facing title regardless of MediaType.
func (r SearchResult) DisplayTitle() string {
	return cmp.Or(r.Title, r.Name)
}

// date is the release date (movie) or first air date (TV), whichever this result
// carries, in TMDB's YYYY-MM-DD form. Empty if TMDB has none.
func (r SearchResult) date() string { return cmp.Or(r.ReleaseDate, r.FirstAirDate) }

// Year returns the 4-digit release/air year, or "" if unavailable.
func (r SearchResult) Year() string {
	if d := r.date(); len(d) >= 4 {
		return d[:4]
	}
	return ""
}

// interleave merges two type-scoped result sets into one list, taking from each
// in turn.
//
// Both halves arrive ranked by the same thing (relevance for a search, trending
// score for a trend), so alternating keeps every row in the order TMDB put it in
// relative to its own kind, which is the only ranking TMDB actually stated.
// Sorting the union by popularity instead discards that and measurably misorders:
// searching "star wars" it drops the original film below The Clone Wars, and
// "avatar" buries the 2009 film under three newer titles.
func interleave(movies, shows []SearchResult) []SearchResult {
	out := make([]SearchResult, 0, len(movies)+len(shows))
	for i := range max(len(movies), len(shows)) {
		if i < len(movies) {
			out = append(out, movies[i])
		}
		if i < len(shows) {
			out = append(out, shows[i])
		}
	}
	return out
}

// Genre is a TMDB genre. IDs are namespaced per media type: movie "Action"
// (28) and TV "Action & Adventure" (10759) are distinct, so genres are always
// carried together with their media type.
type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GenreCatalog holds the genre lists for both castable media types.
type GenreCatalog struct {
	Movie []Genre
	TV    []Genre
}

// For returns the genre list for a media type (MediaMovie / MediaTV).
func (c GenreCatalog) For(mediaType string) []Genre {
	if mediaType == MediaTV {
		return c.TV
	}
	return c.Movie
}

// Details is the superset of /movie/{id} and /tv/{id} fields the metadata
// panel renders. Movie-only and TV-only fields sit side by side; the unused
// side stays zero for a given media type.
type Details struct {
	Tagline        string  `json:"tagline"`
	Runtime        int     `json:"runtime"`          // movie: minutes
	EpisodeRunTime []int   `json:"episode_run_time"` // tv: per-episode minutes
	Genres         []Genre `json:"genres"`
	Credits        struct {
		Cast []struct {
			Name string `json:"name"`
		} `json:"cast"`
	} `json:"credits"`
}

// RuntimeMinutes returns the best single runtime figure: the movie runtime, or
// a TV show's typical episode runtime, or 0 if unknown.
func (d *Details) RuntimeMinutes() int {
	if d.Runtime > 0 {
		return d.Runtime
	}
	if len(d.EpisodeRunTime) > 0 {
		return d.EpisodeRunTime[0]
	}
	return 0
}

// GenreNames returns the genre display names in TMDB order.
func (d *Details) GenreNames() []string {
	names := make([]string, len(d.Genres))
	for i, g := range d.Genres {
		names[i] = g.Name
	}
	return names
}

// TopCast returns up to n billed cast member names.
func (d *Details) TopCast(n int) []string {
	names := make([]string, 0, n)
	for _, member := range d.Credits.Cast {
		if len(names) == n {
			break
		}
		if member.Name != "" {
			names = append(names, member.Name)
		}
	}
	return names
}

// TVDetails is /tv/{id}.
type TVDetails struct {
	Name    string   `json:"name"`
	Seasons []Season `json:"seasons"`
}

// Season is a season summary from TVDetails.
type Season struct {
	SeasonNumber int    `json:"season_number"`
	Name         string `json:"name"`
	EpisodeCount int    `json:"episode_count"`
	AirDate      string `json:"air_date"`
}

// Unaired reports that this season is dated in the future.
//
// The rule is deliberately the reverse of SearchResult.released: it hides only
// what is positively dated ahead, so an undated season stays. A season reached by
// drilling into a show is not an announcement the way a browse row is, and TMDB
// routinely leaves the air date off a season whose episodes carry theirs, so
// reading "no date" as unaired here would hide something watchable. An empty date
// sorts below every real one, which is what leaves it in.
func (s Season) Unaired() bool { return s.AirDate > today() }

// Episode is one entry of /tv/{id}/season/{n}.
type Episode struct {
	EpisodeNumber int    `json:"episode_number"`
	Name          string `json:"name"`
	Overview      string `json:"overview"`
	AirDate       string `json:"air_date"`
}

// Unaired reports that this episode is dated in the future. It reads a date the
// same way Season.Unaired does and for the same reason: a season part way through
// its run lists the episodes still to come alongside the ones already out.
func (e Episode) Unaired() bool { return e.AirDate > today() }

// SeasonDetails is /tv/{id}/season/{n}.
type SeasonDetails struct {
	Episodes []Episode `json:"episodes"`
}

// New builds a Client. timeout==0 falls back to 10s.
func New(apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	// A tuned transport keeps connections warm across the many small calls the
	// browse TUI makes (tops, search, per-hover details, discover pages).
	transport := &http.Transport{
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     60 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	return &Client{
		apiKey: apiKey,
		base:   defaultBase,
		http:   &http.Client{Timeout: timeout, Transport: transport},
	}
}

// Search looks for castable titles by name.
//
// It asks /search/movie and /search/tv rather than the single /search/multi call
// this used to make, because multi also returns people and no argument says
// otherwise: the only way to not receive them is to not ask for them. The two
// type-scoped endpoints return exactly what castor can play, so nothing has to be
// discarded from what comes back.
//
// Neither accepts a release ceiling (they take a release YEAR, which cannot say
// "not yet out"), so a title still in production is returned and shown. That is
// the deliberate consequence of never filtering a response: what castor displays
// is what TMDB was asked for.
func (c *Client) Search(ctx context.Context, query string) ([]SearchResult, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("include_adult", "false")

	var movies, shows []SearchResult
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { movies, err = c.typed(ctx, "/search/movie", q, MediaMovie); return })
	g.Go(func() (err error) { shows, err = c.typed(ctx, "/search/tv", q, MediaTV); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return interleave(movies, shows), nil
}

// TV fetches /tv/{id}.
func (c *Client) TV(ctx context.Context, id int) (*TVDetails, error) {
	var d TVDetails
	if err := c.get(ctx, "/tv/"+strconv.Itoa(id), nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Details fetches /{mediaType}/{id} with credits folded into the same round
// trip via append_to_response.
func (c *Client) Details(ctx context.Context, mediaType string, id int) (*Details, error) {
	q := url.Values{}
	q.Set("append_to_response", "credits")
	var d Details
	if err := c.get(ctx, "/"+mediaType+"/"+strconv.Itoa(id), q, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Genres fetches the movie and TV genre catalogs concurrently.
func (c *Client) Genres(ctx context.Context) (GenreCatalog, error) {
	var cat GenreCatalog
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { cat.Movie, err = c.genreList(ctx, MediaMovie); return })
	g.Go(func() (err error) { cat.TV, err = c.genreList(ctx, MediaTV); return })
	if err := g.Wait(); err != nil {
		return GenreCatalog{}, err
	}
	return cat, nil
}

func (c *Client) genreList(ctx context.Context, mediaType string) ([]Genre, error) {
	var resp struct {
		Genres []Genre `json:"genres"`
	}
	if err := c.get(ctx, "/genre/"+mediaType+"/list", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Genres, nil
}

// Sort selects the ordering for Discover.
type Sort int

const (
	SortPopularity Sort = iota
	SortRating
	SortNewest
)

// Label is the user-facing name of a sort.
func (s Sort) Label() string {
	switch s {
	case SortRating:
		return "Rating"
	case SortNewest:
		return "Newest"
	default:
		return "Popularity"
	}
}

// Sorts is the ordered set of sorts the UI cycles through.
var Sorts = []Sort{SortPopularity, SortRating, SortNewest}

// DiscoverParams drives a /discover query.
type DiscoverParams struct {
	MediaType string // MediaMovie | MediaTV
	GenreIDs  []int  // AND-combined
	Sort      Sort
	Page      int // 1-based; 0 is treated as 1
}

// Page is one page of discover results plus the paging cursor, so callers can
// tell whether more pages remain without a second request.
type Page struct {
	Results    []SearchResult
	Page       int
	TotalPages int
}

// Discover runs /discover/{movie,tv} with a genre filter and sort. MediaType is
// stamped onto every result since /discover responses omit media_type.
func (c *Client) Discover(ctx context.Context, p DiscoverParams) (Page, error) {
	q := url.Values{}
	q.Set("include_adult", "false")
	q.Set("page", strconv.Itoa(max(p.Page, 1)))
	q.Set("sort_by", sortBy(p.Sort, p.MediaType))

	// The release ceiling rides on every discover request rather than being applied
	// to its answer. TMDB drops future-dated rows server-side, so a page comes back
	// holding twenty castable titles instead of twenty minus however many are still
	// months from release, and paging stays honest. A range filter also excludes
	// rows carrying no date at all, which an announced-only title has.
	q.Set(dateField(p.MediaType)+".lte", today())

	if p.MediaType == MediaMovie {
		q.Set("include_video", "false")
	}
	if len(p.GenreIDs) > 0 {
		ids := make([]string, len(p.GenreIDs))
		for i, id := range p.GenreIDs {
			ids[i] = strconv.Itoa(id)
		}
		q.Set("with_genres", strings.Join(ids, ",")) // comma = AND
	}
	if p.Sort == SortRating {
		// Rating without a vote floor surfaces obscure titles with a single
		// 10/10 vote; require a meaningful sample.
		q.Set("vote_count.gte", "300")
	}

	var resp struct {
		Page       int            `json:"page"`
		TotalPages int            `json:"total_pages"`
		Results    []SearchResult `json:"results"`
	}
	if err := c.get(ctx, "/discover/"+p.MediaType, q, &resp); err != nil {
		return Page{}, err
	}
	for i := range resp.Results {
		resp.Results[i].MediaType = p.MediaType
	}
	return Page{Results: resp.Results, Page: resp.Page, TotalPages: resp.TotalPages}, nil
}

func sortBy(s Sort, mediaType string) string {
	switch s {
	case SortRating:
		return "vote_average.desc"
	case SortNewest:
		return dateField(mediaType) + ".desc"
	default:
		return "popularity.desc"
	}
}

func dateField(mediaType string) string {
	if mediaType == MediaTV {
		return "first_air_date"
	}
	return "primary_release_date"
}

func today() string { return time.Now().UTC().Format(time.DateOnly) }

// Trending returns the week's trending movies and shows.
//
// Like Search it asks the two type-scoped endpoints instead of /trending/all/week,
// which mixes in whichever people are trending. Trending is the one thing TMDB
// computes rather than queries and it accepts no arguments at all, so choosing the
// endpoint is the only request-level control there is. No release ceiling is
// available, and unreleased titles trend often, trailers being what makes them
// trend.
func (c *Client) Trending(ctx context.Context) ([]SearchResult, error) {
	var movies, shows []SearchResult
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { movies, err = c.typed(ctx, "/trending/movie/week", nil, MediaMovie); return })
	g.Go(func() (err error) { shows, err = c.typed(ctx, "/trending/tv/week", nil, MediaTV); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return interleave(movies, shows), nil
}

// typed fetches a list from an endpoint that returns a single media type, and
// stamps that type on every row. Such endpoints omit media_type precisely because
// the path implies it, and everything downstream switches on it.
func (c *Client) typed(ctx context.Context, path string, q url.Values, mediaType string) ([]SearchResult, error) {
	var resp struct {
		Results []SearchResult `json:"results"`
	}
	if err := c.get(ctx, path, q, &resp); err != nil {
		return nil, err
	}
	for i := range resp.Results {
		resp.Results[i].MediaType = mediaType
	}
	return resp.Results, nil
}

// PopularMovies returns the most popular movies that are out.
func (c *Client) PopularMovies(ctx context.Context) ([]SearchResult, error) {
	return c.curated(ctx, MediaMovie, SortPopularity)
}

// PopularTV returns the most popular shows that have started airing.
func (c *Client) PopularTV(ctx context.Context) ([]SearchResult, error) {
	return c.curated(ctx, MediaTV, SortPopularity)
}

// TopRatedMovies returns the best-rated movies that are out.
func (c *Client) TopRatedMovies(ctx context.Context) ([]SearchResult, error) {
	return c.curated(ctx, MediaMovie, SortRating)
}

// TopRatedTV returns the best-rated shows that have started airing.
func (c *Client) TopRatedTV(ctx context.Context) ([]SearchResult, error) {
	return c.curated(ctx, MediaTV, SortRating)
}

// curated runs the discover query behind one of TMDB's headline lists.
//
// TMDB publishes /movie/popular and /movie/top_rated as endpoints of their own,
// and they are what these used to call, but those take no filter arguments at
// all: they answer with whatever is popular, films still months from release
// included, and nothing in the request can say otherwise. Asked the same question
// /discover returns the same titles (top-rated identically, popular to within one
// swap) and it does take the ceiling, so the request excludes what castor cannot
// play rather than the answer being trimmed after it arrives.
func (c *Client) curated(ctx context.Context, mediaType string, sort Sort) ([]SearchResult, error) {
	page, err := c.Discover(ctx, DiscoverParams{MediaType: mediaType, Sort: sort})
	if err != nil {
		return nil, err
	}
	return page.Results, nil
}

// Season fetches /tv/{tvID}/season/{seasonNumber}.
func (c *Client) Season(ctx context.Context, tvID, seasonNumber int) (*SeasonDetails, error) {
	var d SeasonDetails
	path := "/tv/" + strconv.Itoa(tvID) + "/season/" + strconv.Itoa(seasonNumber)
	if err := c.get(ctx, path, nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (c *Client) get(ctx context.Context, path string, extra url.Values, out any) error {
	if extra == nil {
		extra = url.Values{}
	}
	extra.Set("api_key", c.apiKey)
	u := c.base + path + "?" + extra.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("tmdb: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tmdb: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("tmdb: %s: status %d", path, resp.StatusCode)
	}

	// Detect gzip by content rather than by the Content-Encoding header:
	// TMDB / its CDN sometimes returns a gzip body without a matching
	// header, which defeats net/http's transparent decompression. Peeking
	// the first 2 bytes for the gzip magic (\x1f\x8b) is reliable and
	// doesn't consume them.
	br := bufio.NewReader(resp.Body)
	var body io.Reader = br
	if peek, _ := br.Peek(2); len(peek) == 2 && peek[0] == 0x1f && peek[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("tmdb: gunzip %s: %w", path, err)
		}
		defer func() { _ = gz.Close() }()
		body = gz
	}

	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("tmdb: decode %s: %w", path, err)
	}
	return nil
}
