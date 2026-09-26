package playlist

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/origin/serving"
	"github.com/stupside/castor/e2e/strategy"
)

// StaleEdge builds a CDN whose edges lag: every nth reload of a media playlist gets the window it served two reloads before, as in `stale-edge: {every: 3}`.
type StaleEdge struct{}

func (StaleEdge) Name() string { return "stale-edge" }

func (StaleEdge) Build(settings yaml.Node) (origin.Behaviour, error) {
	var s stale
	if err := strategy.Decode(settings, &s); err != nil {
		return nil, fmt.Errorf("stale-edge: %w", err)
	}
	if s.Every < 2 {
		return nil, errors.New("stale-edge: want every >= 2")
	}
	return &s, nil
}

type stale struct {
	Every int `yaml:"every"`

	mu     sync.Mutex
	asked  map[string]int
	served map[string][]string
}

func (*stale) Name() string { return "stale-edge" }

func (s *stale) Wrap(next http.Handler, _ origin.Published) http.Handler {
	s.asked, s.served = map[string]int{}, map[string][]string{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Each playlist lags on its own edge, so another rendition's window never stands in for it.
		serving.RewritesPlaylists(next, func(playlist string) (string, bool) {
			if !strings.Contains(playlist, "#EXTINF") {
				return playlist, false
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			s.asked[r.URL.Path]++
			history := s.served[r.URL.Path]
			if s.asked[r.URL.Path]%s.Every == 0 && len(history) >= 2 {
				return history[len(history)-2], true
			}
			s.served[r.URL.Path] = append(history, playlist)
			return playlist, false
		}).ServeHTTP(w, r)
	})
}
