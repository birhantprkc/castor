package cast

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
	"github.com/stupside/castor/services/mediaserver/internal/wire"
)

// Drive is the line to the lent device until the cast ends or its lender leaves; the cast plays on without it.
func (s *Service) Drive(ctx context.Context, req *mediav1.DriveRequest, out *connect.ServerStream[mediav1.DriveResponse]) error {
	c, err := s.find(req.GetCastId())
	if err != nil {
		return err
	}
	if err := c.lend(wire.FromCapabilities(req.GetCapabilities())); err != nil {
		return err
	}
	defer c.line.Leave()
	lent := req.GetDevice()
	slog.InfoContext(c.ctx, "device lent", "id", lent.GetId(), "name", lent.GetName(), "type", lent.GetType(), "address", lent.GetAddress())
	for {
		select {
		case cmd := <-c.line.Outbox():
			if err := out.Send(&mediav1.DriveResponse{Command: cmd}); err != nil {
				return err
			}
		case <-c.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Service) Answer(_ context.Context, req *mediav1.AnswerRequest) (*mediav1.AnswerResponse, error) {
	c, err := s.find(req.GetCastId())
	if err != nil {
		return nil, err
	}
	if !c.line.Answer(req) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no device command %q awaits an answer", req.GetCommandId()))
	}
	return &mediav1.AnswerResponse{}, nil
}
