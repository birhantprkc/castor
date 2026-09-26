package faults

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/origin/serving"
)

// Truncates builds a behaviour cutting each segment's first response short of its length, as in `truncates: 0.5`.
type Truncates struct{}

func (Truncates) Name() string { return "truncates" }

func (Truncates) Build(settings yaml.Node) (origin.Behaviour, error) {
	var fraction float64
	if err := settings.Decode(&fraction); err != nil || fraction <= 0 || fraction >= 1 {
		return nil, fmt.Errorf("truncates: want the fraction of a segment sent, between 0 and 1 exclusive (%v)", err)
	}
	return &truncate{fraction: fraction}, nil
}

type truncate struct {
	fraction float64
	touched  serving.Arrivals
}

func (*truncate) Name() string { return "truncates" }

func (t *truncate) Wrap(next http.Handler, p origin.Published) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !p.IsSegment(r) {
			next.ServeHTTP(w, r)
			return
		}
		if _, first := t.touched.Arrive(r.URL.Path); !first {
			next.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		// The full Content-Length stays declared, so net/http drops the connection and the client sees a premature EOF.
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		body := rec.Body.Bytes()
		_, _ = w.Write(body[:int(t.fraction*float64(len(body)))])
		w.(http.Flusher).Flush()
	})
}
