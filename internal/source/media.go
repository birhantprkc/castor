package source

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/stupside/castor/internal/source/timeline"
)

// Media reads what a timeline lists, with the headers and session its input carries.
type Media struct {
	Client  Client
	Headers http.Header
}

func (m Media) Read(ctx context.Context, uri string, r timeline.Range) (io.ReadCloser, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	return m.Client.Read(ctx, u, m.Headers, r)
}
