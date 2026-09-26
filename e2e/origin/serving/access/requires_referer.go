package access

import (
	"net/http"

	"github.com/stupside/castor/e2e/origin"
)

// RequiresReferer refuses any request that does not say which page embedded it, as hotlink protection does.
type RequiresReferer struct{}

func (RequiresReferer) Name() string { return "requires-referer" }

func (RequiresReferer) Wrap(next http.Handler, _ origin.Published) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Referer() == "" {
			http.Error(w, "hotlinking refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
