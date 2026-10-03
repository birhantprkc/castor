// Package latest is a value read without a lock, each change closing the channel its previous reading handed out.
package latest

import (
	"sync"
	"sync/atomic"
)

// Value is the latest of a T, published to readers that wait for the next one.
type Value[T any] struct {
	mu  sync.Mutex
	now atomic.Pointer[reading[T]]
}

type reading[T any] struct {
	v       T
	changed chan struct{}
}

// New is a Value holding v.
func New[T any](v T) *Value[T] {
	l := &Value[T]{}
	l.now.Store(&reading[T]{v: v, changed: make(chan struct{})})
	return l
}

// Load is the value now, and a channel closed once it changes.
func (l *Value[T]) Load() (T, <-chan struct{}) {
	r := l.now.Load()
	return r.v, r.changed
}

// Update publishes what change makes of the value, unless change declines to.
func (l *Value[T]) Update(change func(T) (T, bool)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	was := l.now.Load()
	next, ok := change(was.v)
	if !ok {
		return
	}
	l.now.Store(&reading[T]{v: next, changed: make(chan struct{})})
	close(was.changed)
}
