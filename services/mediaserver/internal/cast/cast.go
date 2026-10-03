package cast

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/looplab/fsm"

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

// cast is one cast: where it stands, its line to the device, what it serves, and who reads its lines.
type cast struct {
	ctx       context.Context
	id        string
	cancel    context.CancelCauseFunc
	extractor Extractor
	caster    Caster
	source    origin

	// machine moves only inside an update of now, so each move is taken and published as one.
	machine *fsm.FSM
	now     *latest.Value[view]
	done    chan struct{} // closes once the cast has ended

	line       *lend.Line
	deliveries *mediaroute.Deliveries
	logs       *castlog.Feed
}

// newCast awaits its device for undriven, and no longer than parent lasts; lending it one starts the cast.
func newCast(parent context.Context, id string, reach *url.URL, extractor Extractor, caster Caster, src *castorv1.Source) *cast {
	ctx, cancel := context.WithCancelCause(parent)
	c := &cast{
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
	c.ctx = castlog.Into(ctx, c.logs)
	c.machine = c.lifecycle()
	c.now = latest.New(view{status: &castorv1.CastStatus{Phase: c.source.phase()}})
	time.AfterFunc(undriven, func() { c.end(eventAbandon, errUndriven) })
	context.AfterFunc(ctx, func() { c.end(eventAbandon, outcome(ctx, nil)) })
	return c
}

// run finds the source's streams, readies them and casts them on the lent device, then ends with how that went.
func (c *cast) run(caps media.Capabilities) {
	c.end(eventEnd, outcome(c.ctx, c.cast(caps)))
}

func (c *cast) cast(caps media.Capabilities) error {
	streams, err := c.source.streams(c.ctx, c.extractor)
	if err != nil {
		return err
	}
	c.fire(eventMeasure, func(next *view) { next.status.Streams = uint32(len(streams)) })
	ready, err := c.source.ready(c.ctx, c.caster, streams)
	if err != nil {
		return err
	}
	c.fire(eventRank, func(next *view) { next.status.Castable = uint32(len(ready)) })
	device := lend.NewDevice(c.line, caps, c.deliveries.Reached, func() { c.cancel(lend.ErrLenderLeft) })
	return c.caster.Play(c.ctx, device, c.deliveries, ready, c)
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

func (c *cast) Attempting(try int) {
	c.fire(eventAttempt, func(next *view) { next.status.Attempt = uint32(try) })
}

func (c *cast) Revising(strategy, why string) {
	c.fire(eventRevise, func(next *view) {
		next.status.Revision = &castorv1.Revision{Strategy: strategy, Why: why}
	})
}
