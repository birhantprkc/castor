package execute

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/cast/container"
	"github.com/stupside/castor/services/mediaserver/internal/cast/deliver"
	"github.com/stupside/castor/services/mediaserver/internal/cast/health"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// sink is the mechanism a device fetches a served cast from.
type sink interface {
	URL() *url.URL

	Wait(ctx context.Context) error

	Artifact() deliver.Artifact

	Drained() <-chan struct{}

	Audience() health.Audience

	Settled() error

	Close() error
}

// sinkFor opens the mechanism the format's delivery kind names, judged against what the encoder made.
func sinkFor(ctx context.Context, o deliver.Opening, dir string, out io.Reader, made func() media.Progress) (sink, error) {
	switch o.Format.Delivery {
	case container.DeliverSegmented:
		srv, err := deliver.OpenSegments(ctx, o, dir, out)
		if err != nil {
			return nil, err
		}
		return segmentedSink{Segments: srv, made: made}, nil
	case container.DeliverStream:
		srv, err := deliver.OpenStream(ctx, o, dir, out)
		if err != nil {
			return nil, err
		}
		return streamedSink{Stream: srv, made: made}, nil
	default:
		return nil, fmt.Errorf("no delivery mechanism for kind %v", o.Format.Delivery)
	}
}

// spoolSink streams a spool another writes, whole once drained closes, judged against what that writer made.
func spoolSink(ctx context.Context, o deliver.Opening, sp *deliver.Spool, drained <-chan struct{}, made func() media.Progress) (sink, error) {
	srv, err := deliver.OpenSpooledStream(ctx, o, sp, drained)
	if err != nil {
		return nil, err
	}
	return streamedSink{Stream: srv, made: made}, nil
}

// streamedSink judges a progressive stream by the share of what was made that one client took.
type streamedSink struct {
	*deliver.Stream
	made func() media.Progress
}

func (s streamedSink) Audience() health.Audience { return s }

func (s streamedSink) Buffered() time.Duration { return s.made().Position }

func (s streamedSink) Settled() error {
	handed, _ := s.Handed()
	return health.Shortfall(handed, s.made())
}

// segmentedSink can only state whether anything was fetched: its window deletes behind the live edge.
type segmentedSink struct {
	*deliver.Segments
	made func() media.Progress
}

func (segmentedSink) Audience() health.Audience { return nil }

func (s segmentedSink) Settled() error { return health.NoneFetched(s.Served(), s.made()) }
