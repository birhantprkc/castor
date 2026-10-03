// Package faults holds origins whose transfers fail: slow, stalled, cut short, redirected or cold.
package faults

import (
	"fmt"
	"net/http"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/origin/serving"
)

// Trickles builds a behaviour serving segments at a fixed byte rate, as in `trickles: 24576`.
type Trickles struct{}

func (Trickles) Name() string { return "trickles" }

func (Trickles) Build(settings yaml.Node) (origin.Behaviour, error) {
	var rate int
	if err := settings.Decode(&rate); err != nil || rate <= 0 {
		return nil, fmt.Errorf("trickles: want a positive rate in bytes per second (%v)", err)
	}
	return trickle{rate: rate}, nil
}

type trickle struct{ rate int }

func (trickle) Name() string { return "trickles" }

func (t trickle) Wrap(next http.Handler, p origin.Published) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !p.IsSegment(r) {
			next.ServeHTTP(w, r)
			return
		}
		body := serving.Buffered(next, w, r)
		const chunk = 4 << 10
		pause := time.Duration(chunk) * time.Second / time.Duration(t.rate)
		for len(body) > 0 && r.Context().Err() == nil {
			n := min(chunk, len(body))
			if _, err := w.Write(body[:n]); err != nil {
				return
			}
			_ = http.NewResponseController(w).Flush()
			body = body[n:]
			select {
			case <-time.After(pause):
			case <-p.Closing:
				return
			}
		}
	})
}
