package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/source"
)

// ready is req's streams in the order its cast walks them: found ones ranked, a named one measured as is.
func ready(ctx context.Context, caster Caster, req *castorv1.StartRequest) ([]*source.Stream, error) {
	if named := req.GetNamed(); named != nil {
		stream, err := sourceStream(named)
		if err != nil {
			return nil, err
		}
		one, err := caster.Measure(ctx, stream)
		if err != nil {
			return nil, fmt.Errorf("measuring stream: %w", err)
		}
		return []*source.Stream{one}, nil
	}
	found, err := sourceStreams(req.GetFound().GetStreams())
	if err != nil {
		return nil, err
	}
	return caster.Rank(ctx, found)
}

// handed is how many streams req hands its cast.
func handed(req *castorv1.StartRequest) int {
	if req.GetNamed() != nil {
		return 1
	}
	return len(req.GetFound().GetStreams())
}

func sourceStreams(wired []*castorv1.Stream) ([]*source.Stream, error) {
	found := make([]*source.Stream, len(wired))
	for i, st := range wired {
		stream, err := sourceStream(st)
		if err != nil {
			return nil, err
		}
		found[i] = stream
	}
	return found, nil
}

// wireStream is s on the wire, with the headers fetching it needs.
func wireStream(s *source.Stream) *castorv1.Stream {
	headers := make(map[string]string, len(s.Headers))
	for k := range s.Headers {
		headers[k] = s.Headers.Get(k)
	}
	return &castorv1.Stream{Url: s.URL.String(), Headers: headers, ContentType: s.ContentType}
}

// sourceStream is the stream s names; the contract already holds its URL absolute.
func sourceStream(s *castorv1.Stream) (*source.Stream, error) {
	u, err := url.Parse(s.GetUrl())
	if err != nil {
		return nil, fmt.Errorf("stream URL: %w", err)
	}
	var headers http.Header
	if len(s.GetHeaders()) > 0 {
		headers = make(http.Header, len(s.GetHeaders()))
		for k, v := range s.GetHeaders() {
			headers.Set(k, v)
		}
	}
	return &source.Stream{URL: u, Headers: headers, ContentType: s.GetContentType()}, nil
}
