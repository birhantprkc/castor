package hls

import (
	"context"
	"fmt"
	"net/url"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/timeline"
)

// Timeline follows a live media playlist, and a spliced fMP4 one whose init may change at a seam; others are read directly.
func (Format) Timeline(env source.Env, in media.Input, _ media.TrackKind) timeline.Source {
	if !in.Fetch.Live && (!in.Fetch.Spliced || in.Fetch.Framing != media.FramingOutOfBand) {
		return nil
	}
	return follower{Client: env.Client, Headers: in.Headers, url: in.URL}
}

// follower reads the origin's current window of one media playlist, and the media it lists.
type follower struct {
	source.Media
	url *url.URL
}

func (f follower) Window(ctx context.Context) (timeline.Window, error) {
	body, from, status, err := f.Client.Fetch(ctx, f.url, f.Headers)
	if err != nil {
		return timeline.Window{}, &timeline.Failure{Status: status, Err: err}
	}
	if multivariant(body) {
		return timeline.Window{}, fmt.Errorf("%s now names renditions instead of listing segments", f.url.Redacted())
	}
	listed, err := scanMedia(body, from)
	if err != nil {
		return timeline.Window{}, err
	}
	return timeline.Window{Segments: listed.segments, Closed: listed.closed, Start: listed.start}, nil
}
