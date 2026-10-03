package tmdb

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Sort selects the ordering for Discover.
type Sort int

const (
	SortPopularity Sort = iota
	SortRating
	SortNewest
)

type DiscoverParams struct {
	MediaType Media
	GenreIDs  []int // AND-combined
	Sort      Sort
	Page      int // 1-based; 0 is treated as 1
}

// Page carries paging cursor; allows caller to check remaining pages without request.
type Page struct {
	Results    []SearchResult
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
		TotalPages int            `json:"total_pages"`
		Results    []SearchResult `json:"results"`
	}
	if err := c.get(ctx, "/discover/"+string(p.MediaType), q, &resp); err != nil {
		return Page{}, err
	}
	for i := range resp.Results {
		resp.Results[i].MediaType = p.MediaType
	}
	return Page{Results: resp.Results, TotalPages: resp.TotalPages}, nil
}

func sortBy(s Sort, media Media) string {
	switch s {
	case SortRating:
		return "vote_average.desc"
	case SortNewest:
		return dateField(media) + ".desc"
	default:
		return "popularity.desc"
	}
}

func dateField(media Media) string {
	if media == MediaTV {
		return "first_air_date"
	}
	return "primary_release_date"
}

func today() string { return time.Now().UTC().Format(time.DateOnly) }
