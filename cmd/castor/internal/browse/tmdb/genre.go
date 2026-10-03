package tmdb

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// Genre IDs are namespaced per media type; a genre is always with its media type.
type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type GenreCatalog struct {
	Movie []Genre
	TV    []Genre
}

func (c GenreCatalog) For(media Media) []Genre {
	if media == MediaTV {
		return c.TV
	}
	return c.Movie
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

func (c *Client) genreList(ctx context.Context, media Media) ([]Genre, error) {
	var resp struct {
		Genres []Genre `json:"genres"`
	}
	if err := c.get(ctx, "/genre/"+string(media)+"/list", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Genres, nil
}
