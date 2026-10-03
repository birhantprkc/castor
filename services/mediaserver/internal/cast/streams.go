package cast

import (
	"context"

	"connectrpc.com/connect"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// streams readies a source's streams as a cast asked the same would, casting nothing.
type streams struct {
	extractor Extractor
	caster    func(asked *castorv1.Preferences) Caster
}

func (r streams) Rank(ctx context.Context, req *mediav1.RankRequest) (*mediav1.RankResponse, error) {
	src := originOf(req.GetSource())
	found, err := src.streams(ctx, r.extractor)
	if err != nil {
		return nil, failed(ctx, connect.CodeNotFound, err)
	}
	ranked, err := src.ready(ctx, r.caster(req.GetPreferences()), found)
	if err != nil {
		return nil, failed(ctx, connect.CodeFailedPrecondition, err)
	}
	out := &mediav1.RankResponse{Ranked: make([]*castorv1.RankedStream, len(ranked))}
	for i, s := range ranked {
		out.Ranked[i] = &castorv1.RankedStream{Url: s.URL.String(), Bitrate: uint64(s.Bitrate()), LastResort: s.LastResort}
	}
	return out, nil
}

// failed codes err as code, unless the request itself ended, which connect codes from its context.
func failed(ctx context.Context, code connect.Code, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return connect.NewError(code, err)
}
