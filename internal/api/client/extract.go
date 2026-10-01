package client

import (
	"context"
	"fmt"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// Extract has the server open req's pages in its browser and returns the streams they played.
func (c *Client) Extract(ctx context.Context, req *castorv1.ExtractRequest) ([]*castorv1.Stream, error) {
	resp, err := c.extractor.Extract(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("extracting streams: %w", err)
	}
	return resp.GetStreams(), nil
}
