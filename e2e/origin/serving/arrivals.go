package serving

import "sync"

// Arrivals numbers each distinct path by when it was first requested.
type Arrivals struct {
	mu   sync.Mutex
	seen map[string]int
}

// Arrive is the path's arrival number, the same on every retry of it, and whether this request is its first.
func (a *Arrivals) Arrive(p string) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n, ok := a.seen[p]; ok {
		return n, false
	}
	if a.seen == nil {
		a.seen = map[string]int{}
	}
	a.seen[p] = len(a.seen) + 1
	return a.seen[p], true
}
