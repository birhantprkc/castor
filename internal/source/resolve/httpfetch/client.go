// Package httpfetch binds the source layer's playlist reads to net/http.
//
// It is small on purpose. It exists so that reducing an HLS document (which
// variants are castable, which rendition their audio comes from, whether the
// document is a live edge) is exercised over fixture documents with no origin to
// reach, and so the one decision this side owns, what an origin's answer means,
// is made in a single place instead of inside the policy that consumes it.
package httpfetch

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"time"
)

// Client fetches HLS documents over HTTP. It satisfies resolve.Playlists.
type Client struct {
	client *http.Client
}

// New binds a client to the budget one playlist fetch gets. The timeout is on the
// client rather than per request because it must cover the response body too: a
// playlist read from a tarpitting origin that answers headers and then dribbles
// bytes is the shape a context deadline alone would let hang.
func New(timeout time.Duration) *Client {
	return &Client{client: &http.Client{Timeout: timeout}}
}

// Fetch GETs u with h and returns the document body together with the status the
// origin answered with.
//
// The status is reported alongside the error rather than only inside its text,
// because a signed link that answered 403 and a link that never answered at all
// are different facts about the same failure: the first is dead for every reader
// and only a fresh extraction can help, the second is unproven and a retry can.
// A zero status means no answer arrived.
func (c *Client) Fetch(ctx context.Context, u *url.URL, h http.Header) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", 0, fmt.Errorf("creating request: %w", err)
	}
	maps.Copy(req.Header, h)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("fetching playlist: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, fmt.Errorf("fetching playlist: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, fmt.Errorf("reading playlist: %w", err)
	}
	return string(body), resp.StatusCode, nil
}
