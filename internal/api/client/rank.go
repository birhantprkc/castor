package client

import (
	"context"
	"fmt"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// Rank has the server rank streams as a cast asked the same would, without casting; best first.
func (c *Client) Rank(ctx context.Context, req *castorv1.RankRequest) ([]*castorv1.RankedStream, error) {
	resp, err := c.streams.Rank(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ranking streams: %w", err)
	}
	return resp.GetRanked(), nil
}
