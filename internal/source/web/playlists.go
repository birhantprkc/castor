package web

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"time"

	"github.com/stupside/castor/internal/source"
)

// playlists reads the documents a format fetches over net/http; satisfies source.Playlists.
type playlists struct {
	client *http.Client
}

// documentLimit is far above any real playlist or manifest, and far below a film served in its place.
const documentLimit = 16 << 20

func Playlists(timeout time.Duration) source.Playlists {
	return &playlists{client: &http.Client{Timeout: timeout}}
}

func (c *playlists) Fetch(ctx context.Context, u *url.URL, h http.Header) (string, *url.URL, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", u, 0, fmt.Errorf("creating request: %w", err)
	}
	maps.Copy(req.Header, h)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", u, 0, fmt.Errorf("fetching playlist: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	from := u
	if resp.Request != nil && resp.Request.URL != nil {
		from = resp.Request.URL
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", from, resp.StatusCode, fmt.Errorf("fetching playlist: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, documentLimit+1))
	if err != nil {
		return "", from, resp.StatusCode, fmt.Errorf("reading playlist: %w", err)
	}
	if len(body) > documentLimit {
		return "", from, resp.StatusCode, fmt.Errorf("reading playlist: larger than %d bytes", documentLimit)
	}
	return string(body), from, resp.StatusCode, nil
}
