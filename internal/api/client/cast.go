package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// ErrStopped is a cast stopped before it ended, by this client or another.
var ErrStopped = errors.New("cast stopped")

// ErrRefused is a request the server refused as the contract forbids it; the error says what it broke.
var ErrRefused = errors.New("the server refused it")

// refused names a refusal for what it is; the server holds the contract's rules, so the client checks none itself.
func refused(err error) error {
	if e, ok := errors.AsType[*connect.Error](err); ok && e.Code() == connect.CodeInvalidArgument {
		return fmt.Errorf("%w: %s", ErrRefused, e.Message())
	}
	return err
}

// controlTimeout bounds a start, stop or answer, which run past the caller's cancellation so no cast is left unknown.
const controlTimeout = 5 * time.Second

// Start has the server begin the cast req asks for; it plays once a device is lent to it with Drive.
func (c *Client) Start(ctx context.Context, req *castorv1.StartCastRequest) (string, error) {
	// Cancelling mid-start would leave a cast running whose id never arrived to stop it by.
	start, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlTimeout)
	defer cancel()
	started, err := c.casts.StartCast(start, req)
	if err != nil {
		return "", fmt.Errorf("starting cast: %w", refused(err))
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
