package follow

import (
	"context"
	"strconv"
	"sync"

	"golang.org/x/sync/singleflight"
)

// registry names each distinct resource a feed serves, the same name every time it is listed.
type registry[T comparable] struct {
	mu   sync.Mutex
	ids  map[T]int
	held []T
}

func (r *registry[T]) id(v T) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.ids[v]; ok {
		return id
	}
	if r.ids == nil {
		r.ids = map[T]int{}
	}
	r.ids[v] = len(r.held)
	r.held = append(r.held, v)
	return r.ids[v]
}

func (r *registry[T]) lookup(raw string) (T, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, err := strconv.Atoi(raw)
	if err != nil || id < 0 || id >= len(r.held) {
		var zero T
		return zero, false
	}
	return r.held[id], true
}

// resources holds what every segment of a run shares, init sections and keys, read once however many ask at once.
type resources struct {
	flight singleflight.Group
	mu     sync.Mutex
	held   map[string][]byte
	// order is when each was read, so a stream rotating its key every segment keeps only the recent ones.
	order []string
}

// kept is how many init sections and keys a feed holds: far more than one window ever names.
const kept = 64

func (c *resources) get(ctx context.Context, id string, read func(context.Context) ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	b, ok := c.held[id]
	c.mu.Unlock()
	if ok {
		return b, nil
	}
	// One reader's disconnect must not fail the others waiting on the same read; patience still bounds it.
	v, err, _ := c.flight.Do(id, func() (any, error) {
		b, err := read(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.held == nil {
			c.held = map[string][]byte{}
		}
		c.held[id], c.order = b, append(c.order, id)
		if len(c.order) > kept {
			delete(c.held, c.order[0])
			c.order = c.order[1:]
		}
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}
