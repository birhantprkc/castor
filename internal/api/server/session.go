package server

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"time"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

var (
	errStopped    = errors.New("cast stopped")
	errUndriven   = errors.New("no device was lent to this cast")
	errDriverLeft = errors.New("the client that lent the device left")
)

// undriven is how long a cast waits for a device before it gives up.
const undriven = time.Minute

// session is one cast: where it stands, its line to the device, what it serves, and who reads its lines.
type session struct {
	ctx    context.Context
	id     string
	cancel context.CancelCauseFunc
	ended  chan struct{}

	status     *status
	line       *line
	deliveries *deliveries
	logs       logs
}

func newSession(ctx context.Context, id string, cancel context.CancelCauseFunc, server *url.URL) *session {
	s := &session{
		id:         id,
		cancel:     cancel,
		ended:      make(chan struct{}),
		status:     newStatus(),
		line:       newLine(),
		deliveries: newDeliveries(server, id),
		logs:       logs{watchers: map[chan *castorv1.WatchResponse]slog.Level{}},
	}
	// Everything the cast logs carries it, so its lines reach the watchers who asked for them.
	s.ctx = context.WithValue(ctx, castKey{}, s)
	return s
}

func (s *session) end(outcome error) {
	if s.status.end(outcome) {
		close(s.ended)
	}
}

// watch sends the status at once and on every change, and live log lines at level, until the cast is over; it never drives.
func (s *session) watch(ctx context.Context, level slog.Level, logged bool, send func(*castorv1.WatchResponse) error) (outcome, err error) {
	lines := s.logs.subscribe(level, logged)
	defer s.logs.unsubscribe(lines)
	sent := -1
	for {
		changed := s.status.changed.wait()
		now, version, over, outcome := s.status.read()
		if version != sent {
			if err := send(&castorv1.WatchResponse{Update: &castorv1.WatchResponse_Status{Status: now}}); err != nil {
				return nil, err
			}
			sent = version
		}
		if over {
			// Lines queued before the end were logged before it, so they go out before the outcome does.
			for len(lines) > 0 {
				if err := send(<-lines); err != nil {
					return nil, err
				}
			}
			return outcome, nil
		}
		select {
		case <-changed:
		case line := <-lines:
			if err := send(line); err != nil {
				return nil, err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *session) Attempting(try int) {
	s.status.update(func(now *castorv1.CastStatus) {
		now.Phase, now.Attempt = castorv1.CastStatus_PHASE_CASTING, int32(try)
	})
}

func (s *session) Revising(strategy, why string) {
	s.status.update(func(now *castorv1.CastStatus) {
		now.Revision = &castorv1.CastStatus_Revision{Strategy: strategy, Why: why}
	})
}
