package execute

import (
	"errors"
	"sync"
)

// releases undoes what an attempt acquired, last acquired first, so each step owns its own teardown.
type releases struct {
	mu    sync.Mutex
	stack []func() error
}

// push records how to release something just acquired; safe from any goroutine the attempt runs.
func (r *releases) push(release func() error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stack = append(r.stack, release)
}

// release runs every release in reverse, including any pushed while it runs, and joins what they report.
func (r *releases) release() error {
	var errs []error
	for {
		r.mu.Lock()
		if len(r.stack) == 0 {
			r.mu.Unlock()
			return errors.Join(errs...)
		}
		last := r.stack[len(r.stack)-1]
		r.stack = r.stack[:len(r.stack)-1]
		r.mu.Unlock()
		errs = append(errs, last())
	}
}
