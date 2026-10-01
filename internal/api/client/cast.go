package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// ErrStopped is a cast stopped before it ended, by this client or another.
var ErrStopped = errors.New("cast stopped")

// controlTimeout bounds a start, stop or answer, which run past the caller's cancellation so no cast is left unknown.
const controlTimeout = 5 * time.Second

// Start has the server begin the cast req asks for; it plays once a device is lent to it with Drive.
func (c *Client) Start(ctx context.Context, req *castorv1.StartCastRequest) (string, error) {
	// Cancelling mid-start would leave a cast running whose id never arrived to stop it by.
	start, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlTimeout)
	defer cancel()
	started, err := c.casts.StartCast(start, req)
	if err != nil {
		return "", fmt.Errorf("starting cast: %w", err)
	}
	return started.GetCastId(), nil
}

// Stop ends cast id, even when ctx is already cancelled.
func (c *Client) Stop(ctx context.Context, id string) error {
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlTimeout)
	defer cancel()
	if _, err := c.casts.StopCast(stop, &castorv1.StopCastRequest{CastId: id}); err != nil {
		return fmt.Errorf("stopping cast: %w", err)
	}
	return nil
}
