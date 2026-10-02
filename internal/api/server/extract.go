package server

import (
	"context"

	"connectrpc.com/connect"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// extraction finds the streams pages play, with the server's browser.
type extraction struct{ Extractor }

func (e extraction) Extract(ctx context.Context, req *castorv1.ExtractRequest) (*castorv1.ExtractResponse, error) {
	found, err := e.ExtractAll(ctx, req.GetPages())
	if err != nil {
		return nil, failed(ctx, connect.CodeNotFound, err)
	}
	out := &castorv1.ExtractResponse{Streams: make([]*castorv1.Stream, len(found))}
	for i, s := range found {
		out.Streams[i] = wireStream(s)
	}
	return out, nil
}
