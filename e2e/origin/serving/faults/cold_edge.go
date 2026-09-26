package faults

import (
	"fmt"
	"net/http"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/origin/serving"
)

// ColdEdge builds a behaviour failing each path's first request, as a CDN edge filling its cache, as in `cold-edge: 503`.
type ColdEdge struct{}

func (ColdEdge) Name() string { return "cold-edge" }

func (ColdEdge) Build(settings yaml.Node) (origin.Behaviour, error) {
	var status int
	if err := settings.Decode(&status); err != nil || (status != http.StatusTooManyRequests && (status < 500 || status > 599)) {
		return nil, fmt.Errorf("cold-edge: want 429 or a 5xx status (%v)", err)
	}
	return &coldEdge{status: status}, nil
}

type coldEdge struct {
	status int
	warmed serving.Arrivals
}

func (*coldEdge) Name() string { return "cold-edge" }

func (c *coldEdge) Wrap(next http.Handler, _ origin.Published) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, first := c.warmed.Arrive(r.URL.Path); first {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "cold cache", c.status)
			return
		}
		next.ServeHTTP(w, r)
	})
}
