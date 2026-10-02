package server

import (
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
)

// linger keeps a finished cast findable, so a watcher that arrives late still learns how it ended.
const linger = 5 * time.Minute

// registry is the casts this server runs, by id; every service finds its cast here.
type registry struct {
	mu   sync.Mutex
	byID map[string]*session
}

func newRegistry() *registry { return &registry{byID: map[string]*session{}} }

// add keeps s findable until it has lingered past its end.
func (r *registry) add(s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[s.id] = s
	go func() {
		<-s.done
		time.AfterFunc(linger, func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			delete(r.byID, s.id)
		})
	}()
}

func (r *registry) find(id string) (*session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.byID[id]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no cast %q", id))
	}
	return s, nil
}
