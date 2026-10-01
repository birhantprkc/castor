package server

import (
	"sync"

	"google.golang.org/protobuf/proto"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// status is where a cast stands now, and how it ended once it has; it keeps no history.
type status struct {
	changed *signal

	mu      sync.Mutex
	now     *castorv1.CastStatus
	version int
	over    bool
	outcome error
}

func newStatus() *status {
	return &status{changed: newSignal(), now: &castorv1.CastStatus{Phase: castorv1.CastStatus_PHASE_AWAITING_DEVICE}}
}

// update applies change unless the cast is over.
func (s *status) update(change func(*castorv1.CastStatus)) {
	s.mu.Lock()
	if s.over {
		s.mu.Unlock()
		return
	}
	change(s.now)
	s.version++
	s.mu.Unlock()
	s.changed.raise()
}

// end settles the outcome (nil ended, errStopped stopped, else failed), reporting whether this call did.
func (s *status) end(outcome error) bool {
	s.mu.Lock()
	if s.over {
		s.mu.Unlock()
		return false
	}
	s.over, s.outcome = true, outcome
	s.mu.Unlock()
	s.changed.raise()
	return true
}

// read is a copy of the status and its version, and the outcome once the cast is over.
func (s *status) read() (now *castorv1.CastStatus, version int, over bool, outcome error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.CloneOf(s.now), s.version, s.over, s.outcome
}
