package cast

import (
	"context"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
)

// linger keeps a finished cast findable, so a watcher that arrives late still learns how it ended.
const linger = 5 * time.Minute

// registry is the casts this server runs, by id; every service finds its cast here.
type registry struct {
	mu     sync.Mutex
	byID   map[string]*session
	closed bool
	// live counts the casts not yet ended.
	live sync.WaitGroup
}

func newRegistry() *registry { return &registry{byID: map[string]*session{}} }

// add keeps s findable until it has lingered past its end; a registry draining takes no new cast.
func (r *registry) add(s *session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return connect.NewError(connect.CodeUnavailable, errShutdown)
	}
	r.byID[s.id] = s
	r.live.Go(func() {
		<-s.done
		time.AfterFunc(linger, func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			delete(r.byID, s.id)
		})
	})
	return nil
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

// drain takes no more casts and waits for those running to end, or for ctx.
func (r *registry) drain(ctx context.Context) {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	ended := make(chan struct{})
	go func() {
		r.live.Wait()
		close(ended)
	}()
	select {
	case <-ended:
	case <-ctx.Done():
	}
}
