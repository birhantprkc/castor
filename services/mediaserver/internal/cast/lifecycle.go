package cast

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/looplab/fsm"
	"google.golang.org/protobuf/proto"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// A cast's lifecycle: it waits for its device, finds its pages' streams, measures them, casts them, and ends once.
const (
	stateAwaiting   = "awaiting"
	stateExtracting = "extracting"
	stateMeasuring  = "measuring"
	stateCasting    = "casting"
	stateEnded      = "ended"

	// A device lent to a cast of pages starts it extracting, one lent to a cast of a stream starts it measuring.
	eventLendPages  = "lend-pages"
	eventLendStream = "lend-stream"
	eventMeasure    = "measure"
	eventRank       = "rank"
	eventAttempt    = "attempt"
	eventRevise     = "revise"
	eventEnd        = "end"
	eventAbandon    = "abandon"
)

// phases is how the states a cast works in show in its status; awaiting and ended show the phase before them.
var phases = map[string]castorv1.Phase{
	stateExtracting: castorv1.Phase_PHASE_EXTRACTING,
	stateMeasuring:  castorv1.Phase_PHASE_MEASURING,
	stateCasting:    castorv1.Phase_PHASE_CASTING,
}

// snapshot is a cast's status at one moment, and how it ended once it has.
type snapshot struct {
	status *castorv1.CastStatus
	ended  *castorv1.Ended
}

// lifecycle is the moves a cast may make; reaching its end releases the device and everything the cast holds.
func (s *session) lifecycle() *fsm.FSM {
	return fsm.NewFSM(stateAwaiting, fsm.Events{
		{Name: eventLendPages, Src: []string{stateAwaiting}, Dst: stateExtracting},
		{Name: eventLendStream, Src: []string{stateAwaiting}, Dst: stateMeasuring},
		{Name: eventMeasure, Src: []string{stateExtracting, stateMeasuring}, Dst: stateMeasuring},
		{Name: eventRank, Src: []string{stateMeasuring}, Dst: stateMeasuring},
		{Name: eventAttempt, Src: []string{stateMeasuring, stateCasting}, Dst: stateCasting},
		{Name: eventRevise, Src: []string{stateCasting}, Dst: stateCasting},
		{Name: eventEnd, Src: []string{stateExtracting, stateMeasuring, stateCasting}, Dst: stateEnded},
		// Only a cast still awaiting is abandoned, so a grace that runs out as a device is lent changes nothing.
		{Name: eventAbandon, Src: []string{stateAwaiting}, Dst: stateEnded},
	}, fsm.Callbacks{
		"enter_" + stateEnded: func(context.Context, *fsm.Event) {
			close(s.done)
			s.cancel(nil)
		},
	})
}

// fire takes event if the cast's state allows it, then publishes what change makes of its snapshot; it reports whether it took it.
func (s *session) fire(event string, change func(*snapshot)) bool {
	taken := false
	s.now.Update(func(now snapshot) (snapshot, bool) {
		// Never the cast's context: a cancelled one would abort the transition that ends it.
		err := s.machine.Event(context.Background(), event)
		if _, stayed := errors.AsType[fsm.NoTransitionError](err); err != nil && !stayed {
			return now, false
		}
		next := snapshot{status: proto.CloneOf(now.status), ended: now.ended}
		if phase, ok := phases[s.machine.Current()]; ok {
			next.status.Phase = phase
		}
		change(&next)
		taken = true
		return next, true
	})
	return taken
}

// lend gives the cast its device, as its lender connected it, and starts the cast; a cast takes one device, and none once it is over.
func (s *session) lend(caps media.Capabilities) error {
	if !s.fire(s.source.lent(), func(*snapshot) {}) {
		if s.machine.Is(stateEnded) {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("this cast is over"))
		}
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this cast already has its device"))
	}
	go s.run(caps)
	return nil
}

// end ends the cast by event with outcome: nil ended, errStopped stopped, else why it failed.
func (s *session) end(event string, outcome error) {
	ended := &castorv1.Ended{Outcome: castorv1.Outcome_OUTCOME_ENDED}
	switch {
	case errors.Is(outcome, errStopped):
		ended.Outcome = castorv1.Outcome_OUTCOME_STOPPED
	case outcome != nil:
		ended.Outcome, ended.Reason = castorv1.Outcome_OUTCOME_FAILED, outcome.Error()
	}
	s.fire(event, func(next *snapshot) { next.ended = ended })
}
