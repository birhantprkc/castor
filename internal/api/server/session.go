package server

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/looplab/fsm"
	"google.golang.org/protobuf/proto"

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
	caster Caster
	req    *castorv1.StartRequest

	mu        sync.Mutex
	machine   *fsm.FSM
	now       atomic.Pointer[snapshot]
	done      chan struct{} // closes once the cast has ended
	selfFetch bool

	line       *line
	deliveries *deliveries
	logs       logs
}

// newSession awaits its device for undriven, and no longer than ctx lasts; lending it one starts the cast.
func newSession(ctx context.Context, id string, cancel context.CancelCauseFunc, server *url.URL, caster Caster, req *castorv1.StartRequest) *session {
	s := &session{
		id:         id,
		cancel:     cancel,
		caster:     caster,
		req:        req,
		done:       make(chan struct{}),
		line:       newLine(),
		deliveries: newDeliveries(server, id),
		logs:       logs{watchers: map[chan *castorv1.WatchResponse]slog.Level{}},
	}
	// Everything the cast logs carries it, so its lines reach the watchers who asked for them.
	s.ctx = context.WithValue(ctx, castKey{}, s)
	s.machine = s.lifecycle()
	s.now.Store(&snapshot{status: &castorv1.CastStatus{}, changed: make(chan struct{})})
	time.AfterFunc(undriven, func() { s.end(eventAbandon, errUndriven) })
	context.AfterFunc(ctx, func() { s.end(eventAbandon, outcome(ctx, nil)) })
	return s
}

// run measures the streams and casts them on the lent device, then ends with how that went.
func (s *session) run() {
	ready, err := ready(s.ctx, s.caster, s.req)
	if err == nil {
		s.fire(eventRank, func(next *snapshot) { next.status.Castable = uint32(len(ready)) })
		err = s.caster.Play(s.ctx, remoteRenderer{s: s}, s.deliveries, ready, s)
	}
	s.end(eventEnd, outcome(s.ctx, err))
}

// outcome is how a cast that returned err ended: why its context ended if it did, stopped when nobody said why.
func outcome(ctx context.Context, err error) error {
	switch cause := context.Cause(ctx); {
	case cause == nil:
		return err
	case errors.Is(cause, context.Canceled):
		return errStopped
	default:
		return cause
	}
}

// watch sends the status at once and on every change, and live log lines at level, until the cast is over; it never drives.
func (s *session) watch(ctx context.Context, level slog.Level, logged bool, send func(*castorv1.WatchResponse) error) (outcome, err error) {
	lines := s.logs.subscribe(level, logged)
	defer s.logs.unsubscribe(lines)
	var sent *castorv1.CastStatus
	for {
		now := s.now.Load()
		if !proto.Equal(now.status, sent) {
			if err := send(&castorv1.WatchResponse{Update: &castorv1.WatchResponse_Status{Status: now.status}}); err != nil {
				return nil, err
			}
			sent = now.status
		}
		if now.over {
			// Lines queued before the end were logged before it, so they go out before the outcome does.
			for len(lines) > 0 {
				if err := send(<-lines); err != nil {
					return nil, err
				}
			}
			return now.outcome, nil
		}
		select {
		case <-now.changed:
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
	s.fire(eventAttempt, func(next *snapshot) { next.status.Attempt = uint32(try) })
}

func (s *session) Revising(strategy, why string) {
	s.fire(eventRevise, func(next *snapshot) {
		next.status.Revision = &castorv1.CastStatus_Revision{Strategy: strategy, Why: why}
	})
}
