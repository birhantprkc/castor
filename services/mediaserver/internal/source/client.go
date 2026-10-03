package source

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/stupside/castor/services/mediaserver/internal/source/timeline"
)

// Client reads an origin over HTTP: its documents, and the media they list, on the session the documents opened.
type Client interface {
	Fetch(ctx context.Context, u *url.URL, h http.Header) (body string, from *url.URL, status int, err error)
	// Replay is h as a reader outside this client must send it to u, carrying the session the origin set while documents were read.
	Replay(u *url.URL, h http.Header) http.Header
	// Read opens media bytes, within r when it has a length, on the same session; a status the origin answered comes as a timeline.Failure.
	Read(ctx context.Context, u *url.URL, h http.Header, r timeline.Range) (io.ReadCloser, error)
}

// Upstream reads what a timeline lists, with the headers and session its input carries.
type Upstream struct {
	Client  Client
	Headers http.Header
}

func (u Upstream) Read(ctx context.Context, uri string, r timeline.Range) (io.ReadCloser, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	return u.Client.Read(ctx, parsed, u.Headers, r)
}
