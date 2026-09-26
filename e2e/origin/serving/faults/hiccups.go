package faults

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/origin/serving"
	"github.com/stupside/castor/e2e/strategy"
)

// Hiccups builds a behaviour answering every nth distinct segment late, as in `hiccups: {every: 6, for: 2500ms}`.
type Hiccups struct{}

func (Hiccups) Name() string { return "hiccups" }

func (Hiccups) Build(settings yaml.Node) (origin.Behaviour, error) {
	var h hiccup
	if err := strategy.Decode(settings, &h); err != nil {
		return nil, fmt.Errorf("hiccups: %w", err)
	}
	if h.Every < 1 || h.For <= 0 {
		return nil, errors.New("hiccups: want every >= 1 and a positive for")
	}
	return &h, nil
}

type hiccup struct {
	Every int           `yaml:"every"`
	For   time.Duration `yaml:"for"`
	seen  serving.Arrivals
}

func (*hiccup) Name() string { return "hiccups" }

func (h *hiccup) Wrap(next http.Handler, p origin.Published) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !p.IsSegment(r) {
			next.ServeHTTP(w, r)
			return
		}
		if n, first := h.seen.Arrive(r.URL.Path); first && n%h.Every == 0 {
			select {
			case <-time.After(h.For):
			case <-r.Context().Done():
				return
			case <-p.Closing:
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
