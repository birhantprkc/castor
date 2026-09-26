package access

import (
	"net/http"

	"github.com/stupside/castor/e2e/origin"
)

// RefusesSegments answers every segment with 403, as a CDN whose token expired.
type RefusesSegments struct{}

func (RefusesSegments) Name() string { return "refuses-segments" }

func (RefusesSegments) Wrap(next http.Handler, p origin.Published) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.IsSegment(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
