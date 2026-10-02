package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/looplab/fsm"
	"google.golang.org/protobuf/proto"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// A cast's lifecycle: it waits for its device, measures its streams, casts them, and ends once.
const (
	stateAwaiting  = "awaiting"
	stateMeasuring = "measuring"
	stateCasting   = "casting"
	stateEnded     = "ended"

	eventLend    = "lend"
	eventRank    = "rank"
	eventAttempt = "attempt"
	eventRevise  = "revise"
	eventEnd     = "end"
	eventAbandon = "abandon"
)

// snapshot is a cast's status at one moment; changed closes when the next replaces it.
type snapshot struct {
	status  *castorv1.CastStatus
	over    bool
	outcome error
	changed chan struct{}
}

// lifecycle is the moves a cast may make; reaching its end releases the device and everything the cast holds.
func (s *session) lifecycle() *fsm.FSM {
	return fsm.NewFSM(stateAwaiting, fsm.Events{
		{Name: eventLend, Src: []string{stateAwaiting}, Dst: stateMeasuring},
		{Name: eventRank, Src: []string{stateMeasuring}, Dst: stateMeasuring},
		{Name: eventAttempt, Src: []string{stateMeasuring, stateCasting}, Dst: stateCasting},
		{Name: eventRevise, Src: []string{stateCasting}, Dst: stateCasting},
		{Name: eventEnd, Src: []string{stateMeasuring, stateCasting}, Dst: stateEnded},
		// Only a cast still awaiting is abandoned, so a grace that runs out as a device is lent changes nothing.
		{Name: eventAbandon, Src: []string{stateAwaiting}, Dst: stateEnded},
	}, fsm.Callbacks{
		"enter_" + stateEnded: func(context.Context, *fsm.Event) {
			close(s.done)
			s.cancel(nil)
		},
	})
}

// fire takes event if the cast's state allows it, then publishes what change makes of its snapshot.
func (s *session) fire(event string, change func(*snapshot)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Never the cast's context: a cancelled one would abort the transition that ends it.
	err := s.machine.Event(context.Background(), event)
	if _, stayed := errors.AsType[fsm.NoTransitionError](err); err != nil && !stayed {
		return false
	}
	next := *s.now.Load()
	next.status = proto.CloneOf(next.status)
	next.over = s.machine.Is(stateEnded)
	next.changed = make(chan struct{})
	change(&next)
	close(s.now.Swap(&next).changed)
	return true
}

// lend gives the cast its device and starts it; a cast takes one device, and none once it is over.
func (s *session) lend(selfFetch bool) error {
	if !s.fire(eventLend, func(next *snapshot) { next.status.Streams = uint32(handed(s.req)) }) {
		if s.machine.Is(stateEnded) {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("this cast is over"))
		}
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this cast already has its device"))
	}
	s.selfFetch = selfFetch
	go s.run()
	return nil
}

// end ends the cast by event with outcome: nil ended, errStopped stopped, else why it failed.
func (s *session) end(event string, outcome error) {
	s.fire(event, func(next *snapshot) { next.outcome = outcome })
}
