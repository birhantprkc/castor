// Package registry keeps what a server runs by id, findable for a while after it ends, until the server drains.
package registry

import (
	"context"
	"iter"
	"maps"
	"slices"
	"sync"
	"time"
)

// Registry holds values by id, each findable until linger after it is done.
type Registry[V any] struct {
	linger time.Duration

	mu     sync.Mutex
	byID   map[string]V
	closed bool
	live   sync.WaitGroup
}

// New is an empty Registry whose values linger after they are done.
func New[V any](linger time.Duration) *Registry[V] {
	return &Registry[V]{linger: linger, byID: map[string]V{}}
}

// Add keeps v under id until linger after done closes, unless the registry is draining.
func (r *Registry[V]) Add(id string, v V, done <-chan struct{}) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.byID[id] = v
	r.live.Go(func() {
		<-done
		time.AfterFunc(r.linger, func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			delete(r.byID, id)
		})
	})
	return true
}

// Find is the value kept under id.
func (r *Registry[V]) Find(id string) (V, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.byID[id]
	return v, ok
}

// All is every value kept now.
func (r *Registry[V]) All() iter.Seq[V] {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Values(slices.Collect(maps.Values(r.byID)))
}

// Drain takes no more values and waits until every one is done, or ctx ends.
func (r *Registry[V]) Drain(ctx context.Context) {
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
