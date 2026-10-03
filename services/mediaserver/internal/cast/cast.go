package cast

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/looplab/fsm"
	"google.golang.org/protobuf/proto"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/latest"
	"github.com/stupside/castor/services/mediaserver/internal/castlog"
	"github.com/stupside/castor/services/mediaserver/internal/lend"
	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/mediaroute"
)

var (
	errStopped  = errors.New("cast stopped")
	errUndriven = errors.New("no device was lent to this cast")
)

// undriven is how long a cast waits for a device before it gives up.
const undriven = time.Minute

// session is one cast: where it stands, its line to the device, what it serves, and who reads its lines.
type session struct {
	ctx       context.Context
	id        string
	cancel    context.CancelCauseFunc
	extractor Extractor
	caster    Caster
	source    origin

	// machine moves only inside an update of now, so each move is taken and published as one.
	machine *fsm.FSM
	now     *latest.Value[snapshot]
	done    chan struct{} // closes once the cast has ended

	line       *lend.Line
	deliveries *mediaroute.Deliveries
	logs       *castlog.Feed
}

// newSession awaits its device for undriven, and no longer than parent lasts; lending it one starts the cast.
func newSession(parent context.Context, id string, reach *url.URL, extractor Extractor, caster Caster, src *castorv1.Source) *session {
	ctx, cancel := context.WithCancelCause(parent)
	s := &session{
		id:         id,
		cancel:     cancel,
		extractor:  extractor,
		caster:     caster,
		source:     originOf(src),
		done:       make(chan struct{}),
		line:       lend.NewLine(),
		deliveries: mediaroute.NewDeliveries(reach, id),
		logs:       castlog.NewFeed(),
	}
	// Everything the cast logs carries its feed, so its lines reach the watchers who asked for them.
	s.ctx = castlog.Into(ctx, s.logs)
	s.machine = s.lifecycle()
	s.now = latest.New(snapshot{status: &castorv1.CastStatus{Phase: s.source.phase()}})
	time.AfterFunc(undriven, func() { s.end(eventAbandon, errUndriven) })
	context.AfterFunc(ctx, func() { s.end(eventAbandon, outcome(ctx, nil)) })
	return s
}

// run finds the source's streams, readies them and casts them on the lent device, then ends with how that went.
func (s *session) run(caps media.Capabilities) {
	s.end(eventEnd, outcome(s.ctx, s.cast(caps)))
}

func (s *session) cast(caps media.Capabilities) error {
	streams, err := s.source.streams(s.ctx, s.extractor)
	if err != nil {
		return err
	}
	s.fire(eventMeasure, func(next *snapshot) { next.status.Streams = uint32(len(streams)) })
	ready, err := s.source.ready(s.ctx, s.caster, streams)
	if err != nil {
		return err
	}
	s.fire(eventRank, func(next *snapshot) { next.status.Castable = uint32(len(ready)) })
	device := lend.NewDevice(s.line, caps, s.deliveries.Reached, func() { s.cancel(lend.ErrLenderLeft) })
	return s.caster.Play(s.ctx, device, s.deliveries, ready, s)
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

// watch sends the status at once and on every change, live log lines from logs on (nil asks none), and how the cast ended last; it never drives.
func (s *session) watch(ctx context.Context, logs *castorv1.LogLevel, send func(*castorv1.WatchResponse) error) error {
	lines := s.logs.Subscribe(logs)
	defer s.logs.Unsubscribe(lines)
	var sent *castorv1.CastStatus
	for {
		now, changed := s.now.Load()
		if !proto.Equal(now.status, sent) {
			if err := send(&castorv1.WatchResponse{Update: &castorv1.WatchResponse_Status{Status: now.status}}); err != nil {
				return err
			}
			sent = now.status
		}
		if now.ended != nil {
			// Lines queued before the end were logged before it, so they go out before it does.
			for len(lines) > 0 {
				if err := send(<-lines); err != nil {
					return err
				}
			}
			return send(&castorv1.WatchResponse{Update: &castorv1.WatchResponse_Ended{Ended: now.ended}})
		}
		select {
		case <-changed:
		case line := <-lines:
			if err := send(line); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *session) Attempting(try int) {
	s.fire(eventAttempt, func(next *snapshot) { next.status.Attempt = uint32(try) })
}

func (s *session) Revising(strategy, why string) {
	s.fire(eventRevise, func(next *snapshot) {
		next.status.Revision = &castorv1.Revision{Strategy: strategy, Why: why}
	})
}
