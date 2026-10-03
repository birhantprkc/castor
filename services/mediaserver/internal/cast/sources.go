package cast

import (
	"context"
	"fmt"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/services/mediaserver/internal/source"
	"github.com/stupside/castor/services/mediaserver/internal/wire"
)

// origin is what a cast's source is: pages to search in this server's browser, or the one stream it names.
type origin interface {
	// phase is what the cast shows while it awaits its device, so its status never goes back.
	phase() castorv1.Phase
	// lent is the move lending the cast its device makes.
	lent() string
	streams(ctx context.Context, extractor Extractor) ([]*source.Stream, error)
	// ready is streams in the order the cast walks them.
	ready(ctx context.Context, caster Caster, streams []*source.Stream) ([]*source.Stream, error)
}

func originOf(src *castorv1.Source) origin {
	if p := src.GetPages(); p != nil {
		return pages{urls: p.GetUrls()}
	}
	return stream{src.GetStream()}
}

// pages are found in this server's browser, so all they carried reaches ranking.
type pages struct{ urls []string }

func (pages) phase() castorv1.Phase { return castorv1.Phase_PHASE_EXTRACTING }

func (pages) lent() string { return eventLendPages }

func (p pages) streams(ctx context.Context, extractor Extractor) ([]*source.Stream, error) {
	found, err := extractor.ExtractAll(ctx, p.urls)
	if err != nil {
		return nil, fmt.Errorf("finding streams: %w", err)
	}
	return found, nil
}

func (pages) ready(ctx context.Context, caster Caster, streams []*source.Stream) ([]*source.Stream, error) {
	return caster.Rank(ctx, streams)
}

// stream is measured as is, never ranked.
type stream struct{ *castorv1.Stream }

func (stream) phase() castorv1.Phase { return castorv1.Phase_PHASE_MEASURING }

func (stream) lent() string { return eventLendStream }

func (s stream) streams(context.Context, Extractor) ([]*source.Stream, error) {
	one, err := wire.FromStream(s.Stream)
	if err != nil {
		return nil, err
	}
	return []*source.Stream{one}, nil
}

func (stream) ready(ctx context.Context, caster Caster, streams []*source.Stream) ([]*source.Stream, error) {
	one, err := caster.Measure(ctx, streams[0])
	if err != nil {
		return nil, fmt.Errorf("measuring stream: %w", err)
	}
	return []*source.Stream{one}, nil
}
