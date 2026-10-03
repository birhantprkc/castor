package source

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/stupside/castor/services/mediaserver/internal/source/timeline"
)

// Upstream reads what a timeline lists, with the headers and session its input carries.
type Upstream struct {
	Client  Client
	Headers http.Header
}

func (m Upstream) Read(ctx context.Context, uri string, r timeline.Range) (io.ReadCloser, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	return m.Client.Read(ctx, u, m.Headers, r)
}
