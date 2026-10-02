package server

import (
	"context"

	"connectrpc.com/connect"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// ranking ranks streams as a cast asked the same would, casting nothing.
type ranking struct {
	caster func(asked *castorv1.Preferences) Caster
}

func (r ranking) Rank(ctx context.Context, req *castorv1.RankRequest) (*castorv1.RankResponse, error) {
	found, err := sourceStreams(req.GetStreams())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	ranked, err := r.caster(req.GetPreferences()).Rank(ctx, found)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	out := &castorv1.RankResponse{Ranked: make([]*castorv1.RankedStream, len(ranked))}
	for i, s := range ranked {
		out.Ranked[i] = &castorv1.RankedStream{Url: s.URL.String(), Bitrate: uint64(s.Bitrate()), LastResort: s.LastResort}
	}
	return out, nil
}
