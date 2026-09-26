package faults

import (
	"net/http"

	"github.com/stupside/castor/e2e/origin"
)

// Tarpit accepts every segment request and never answers it.
type Tarpit struct{}

func (Tarpit) Name() string { return "tarpit" }

func (Tarpit) Wrap(next http.Handler, p origin.Published) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.IsSegment(r) {
			p.Held(r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
