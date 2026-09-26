// Package tmdb is a minimal TMDB v3 API client for browse endpoints only.
package tmdb

import (
	"bufio"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	apiBase        = "https://api.themoviedb.org/3"
	imageBase      = "https://image.tmdb.org/t/p/"
	requestTimeout = 10 * time.Second

	// MediaMovie and MediaTV are castable types; path segments and discriminators.
	MediaMovie = "movie"
	MediaTV    = "tv"
)

type Client struct {
	apiKey string
	base   string
	images string
	http   *http.Client
}

// SearchResult is one API result; caller picks title/date pair based on MediaType.
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

func (r SearchResult) DisplayTitle() string {
	return cmp.Or(r.Title, r.Name)
}

// date is the release date (movie) or first air date (TV) in TMDB's YYYY-MM-DD form, or "".
func (r SearchResult) date() string { return cmp.Or(r.ReleaseDate, r.FirstAirDate) }

// Year is the 4-digit release/air year, or "" if TMDB stated no date.
func (r SearchResult) Year() string {
	if d := r.date(); len(d) >= 4 {
		return d[:4]
	}
	return ""
}

// interleave preserves TMDB's per-type ranking better than sorting union by popularity.
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

// Genre IDs are namespaced per media type; a genre is always with its media type.
type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type GenreCatalog struct {
	Movie []Genre
	TV    []Genre
}

func (c GenreCatalog) For(mediaType string) []Genre {
	if mediaType == MediaTV {
		return c.TV
	}
	return c.Movie
}

// Details is superset of /movie/{id} and /tv/{id}; inapplicable side stays zero.
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

// RuntimeMinutes is the movie runtime, or a show's typical episode runtime, or 0 if unknown.
func (d *Details) RuntimeMinutes() int {
	if d.Runtime > 0 {
		return d.Runtime
	}
	if len(d.EpisodeRunTime) > 0 {
		return d.EpisodeRunTime[0]
	}
	return 0
}

func (d *Details) GenreNames() []string {
	names := make([]string, len(d.Genres))
	for i, g := range d.Genres {
		names[i] = g.Name
	}
	return names
}

// TopCast is up to n billed cast member names.
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

type TVDetails struct {
	Name    string   `json:"name"`
	Seasons []Season `json:"seasons"`
}

type Season struct {
	SeasonNumber int    `json:"season_number"`
	Name         string `json:"name"`
	EpisodeCount int    `json:"episode_count"`
	AirDate      string `json:"air_date"`
}

// Unaired hides only positively future-dated; undated stays (episodes may have dates).
func (s Season) Unaired() bool { return s.AirDate > today() }

type Episode struct {
	EpisodeNumber int    `json:"episode_number"`
	Name          string `json:"name"`
	Overview      string `json:"overview"`
	AirDate       string `json:"air_date"`
}

// Unaired reads date as Season.Unaired; handles partial-run seasons.
func (e Episode) Unaired() bool { return e.AirDate > today() }

type SeasonDetails struct {
	Episodes []Episode `json:"episodes"`
}

func New(apiKey string) *Client {
	// Keep connections warm for TUI's many small calls (search, details, discover).
	transport := &http.Transport{
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     60 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	return &Client{
		apiKey: apiKey,
		base:   apiBase,
		images: imageBase,
		http:   &http.Client{Timeout: requestTimeout, Transport: transport},
	}
}

// Search uses /search/movie and /search/tv (not /multi which includes people).
func (c *Client) Search(ctx context.Context, query string) ([]SearchResult, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("include_adult", "false")

	var movies, shows []SearchResult
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { movies, err = c.typed(ctx, "/search/"+MediaMovie, q, MediaMovie); return })
	g.Go(func() (err error) { shows, err = c.typed(ctx, "/search/"+MediaTV, q, MediaTV); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return interleave(movies, shows), nil
}

func (c *Client) TV(ctx context.Context, id int) (*TVDetails, error) {
	var d TVDetails
	if err := c.get(ctx, "/"+MediaTV+"/"+strconv.Itoa(id), nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Details fetches /{mediaType}/{id} with credits folded into the same round trip.
func (c *Client) Details(ctx context.Context, mediaType string, id int) (*Details, error) {
	q := url.Values{}
	q.Set("append_to_response", "credits")
	var d Details
	if err := c.get(ctx, "/"+mediaType+"/"+strconv.Itoa(id), q, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

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

type DiscoverParams struct {
	MediaType string // MediaMovie | MediaTV
	GenreIDs  []int  // AND-combined
	Sort      Sort
	Page      int // 1-based; 0 is treated as 1
}

// Page carries paging cursor; allows caller to check remaining pages without request.
type Page struct {
	Results    []SearchResult
	Page       int
	TotalPages int
}

// Discover stamps MediaType onto every result, since /discover responses omit media_type.
func (c *Client) Discover(ctx context.Context, p DiscoverParams) (Page, error) {
	q := url.Values{}
	q.Set("include_adult", "false")
	q.Set("page", strconv.Itoa(max(p.Page, 1)))
	q.Set("sort_by", sortBy(p.Sort, p.MediaType))

	// Ceiling on request filters server-side; ensures paging stays honest.
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
		// Rating without a vote floor surfaces obscure titles with a single 10/10 vote.
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

// Trending uses /trending/{movie,tv}/week (not /all/week which includes people).
func (c *Client) Trending(ctx context.Context) ([]SearchResult, error) {
	var movies, shows []SearchResult
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		movies, err = c.typed(ctx, "/trending/"+MediaMovie+"/week", nil, MediaMovie)
		return
	})
	g.Go(func() (err error) { shows, err = c.typed(ctx, "/trending/"+MediaTV+"/week", nil, MediaTV); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return interleave(movies, shows), nil
}

// typed adds media_type to results from single-type endpoints (path implies it).
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

func (c *Client) Season(ctx context.Context, tvID, seasonNumber int) (*SeasonDetails, error) {
	var d SeasonDetails
	path := "/" + MediaTV + "/" + strconv.Itoa(tvID) + "/season/" + strconv.Itoa(seasonNumber)
	if err := c.get(ctx, path, nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Poster streams the image at size (w92, w154, w185, w342, w500, original); caller closes it.
func (c *Client) Poster(ctx context.Context, posterPath, size string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.images+size+posterPath, nil)
	if err != nil {
		return nil, fmt.Errorf("tmdb: build poster request: %w", err)
	}
	return c.open(req, "poster "+posterPath)
}

func (c *Client) get(ctx context.Context, path string, extra url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("tmdb: build request: %w", err)
	}
	// Clone values; Search hands same map to goroutines, in-place key set is racy.
	q := maps.Clone(extra)
	if q == nil {
		q = url.Values{}
	}
	q.Set("api_key", c.apiKey)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Accept", "application/json")

	rc, err := c.open(req, path)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	// Detect gzip by content; TMDB CDN returns gzip without Content-Encoding header.
	br := bufio.NewReader(rc)
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

// open returns a 2xx body; its errors name what, never the URL, which carries api_key.
func (c *Client) open(req *http.Request, what string) (io.ReadCloser, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, fmt.Errorf("tmdb: %s: %w", what, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("tmdb: %s: status %d", what, resp.StatusCode)
	}
	return resp.Body, nil
}
