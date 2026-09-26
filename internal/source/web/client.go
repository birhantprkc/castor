// Package web is the HTTP client castor reads origins with: documents, media, and the session they share.
package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"time"

	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/timeline"
	"golang.org/x/net/publicsuffix"
)

// client reads an origin over net/http with the session its documents opened; satisfies source.Client.
type client struct {
	http *http.Client
}

// documentLimit is far above any real playlist or manifest, and far below a film served in its place.
const documentLimit = 16 << 20

func Client(timeout time.Duration) source.Client {
	// A CDN that authorises a session on the master (Akamai's hdntl) refuses every later read without its cookie.
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	return &client{http: &http.Client{Timeout: timeout, Jar: jar}}
}

func (c *client) Session(u *url.URL) http.Header {
	cookies := c.http.Jar.Cookies(u)
	if len(cookies) == 0 {
		return nil
	}
	return http.Header{"Cookie": {source.CookieHeader(cookies)}}
}

func (c *client) Fetch(ctx context.Context, u *url.URL, h http.Header) (string, *url.URL, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", u, 0, fmt.Errorf("creating request: %w", err)
	}
	maps.Copy(req.Header, h)
	withoutJarred(req, c.http.Jar.Cookies(u))

	resp, err := c.http.Do(req)
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

// Read opens media bytes with the headers and session a document read carries, whole or within r.
func (c *client) Read(ctx context.Context, u *url.URL, h http.Header, r timeline.Range) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	maps.Copy(req.Header, h)
	withoutJarred(req, c.http.Jar.Cookies(u))
	if r.Length > 0 {
		req.Header.Set("Range", r.Header())
	}
	// Media is read for as long as it takes to arrive, bounded by the caller rather than the document timeout.
	reader := *c.http
	reader.Timeout = 0
	resp, err := reader.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching media: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, &timeline.Failure{Status: resp.StatusCode, Err: errors.New("fetching media")}
	}
	// An origin that ignores Range answers the whole resource, of which only r is wanted.
	if r.Length > 0 && resp.StatusCode == http.StatusOK {
		if _, err := io.CopyN(io.Discard, resp.Body, r.Offset); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("skipping to byte %d: %w", r.Offset, err)
		}
		return struct {
			io.Reader
			io.Closer
		}{io.LimitReader(resp.Body, r.Length), resp.Body}, nil
	}
	return resp.Body, nil
}

// withoutJarred drops from a request's Cookie header each name the jar will send itself, fresher than a copy taken earlier.
func withoutJarred(req *http.Request, jarred []*http.Cookie) {
	stated := req.Cookies()
	if len(stated) == 0 || len(jarred) == 0 {
		return
	}
	req.Header.Del("Cookie")
	for _, c := range stated {
		if !slices.ContainsFunc(jarred, func(j *http.Cookie) bool { return j.Name == c.Name }) {
			req.AddCookie(c)
		}
	}
}
