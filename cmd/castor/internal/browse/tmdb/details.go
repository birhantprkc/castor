package tmdb

import (
	"context"
	"net/url"
	"strconv"
)

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

// Details fetches /{media}/{id} with credits folded into the same round trip.
func (c *Client) Details(ctx context.Context, media Media, id int) (*Details, error) {
	q := url.Values{"append_to_response": {"credits"}}
	var d Details
	if err := c.get(ctx, "/"+string(media)+"/"+strconv.Itoa(id), q, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
