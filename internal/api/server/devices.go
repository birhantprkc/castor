package server

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/api/wire"
)

// devices takes the device a client lends a cast, and its answers to what the cast asks of it.
type devices struct{ registry *registry }

// Drive is the line to the lent device until the cast ends or its client leaves; the cast plays on without it.
func (d devices) Drive(ctx context.Context, req *castorv1.DriveRequest, out *connect.ServerStream[castorv1.DriveResponse]) error {
	s, err := d.registry.find(req.GetCastId())
	if err != nil {
		return err
	}
	if err := s.line.attach(wire.FromCapabilities(req.GetProfile())); err != nil {
		return err
	}
	defer s.line.leave()
	device := req.GetDevice()
	slog.InfoContext(s.ctx, "device lent", "name", device.GetName(), "type", device.GetType(), "address", device.GetAddress())
	for {
		select {
		case cmd := <-s.line.outbox:
			if err := out.Send(&castorv1.DriveResponse{Command: cmd}); err != nil {
				return err
			}
		case <-s.ended:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (d devices) Answer(_ context.Context, req *castorv1.AnswerRequest) (*castorv1.AnswerResponse, error) {
	s, err := d.registry.find(req.GetCastId())
	if err != nil {
		return nil, err
	}
	if !s.line.answer(req) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no device command %q awaits an answer", req.GetCommandId()))
	}
	return &castorv1.AnswerResponse{}, nil
}
