package browse

import (
	"context"
	"io"

	"github.com/stupside/castor/cmd/castor/internal/browse/tmdb"
)

// Catalog is the title lookups the browser makes: TMDB in use, a test seam so screens render from a fixed shelf.
type Catalog interface {
	Search(ctx context.Context, query string) ([]tmdb.SearchResult, error)
	Trending(ctx context.Context) ([]tmdb.SearchResult, error)
	Discover(ctx context.Context, p tmdb.DiscoverParams) (tmdb.Page, error)
	Genres(ctx context.Context) (tmdb.GenreCatalog, error)
	Details(ctx context.Context, media tmdb.Media, id int) (*tmdb.Details, error)
	TV(ctx context.Context, id int) (*tmdb.TVDetails, error)
	Season(ctx context.Context, tvID, seasonNumber int) (*tmdb.SeasonDetails, error)
	Poster(ctx context.Context, posterPath string) (io.ReadCloser, error)
}
