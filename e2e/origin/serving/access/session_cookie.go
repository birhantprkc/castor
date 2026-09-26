// Package access holds origins that decide who may read them: cookies, referers, signatures and refusals.
package access

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/stupside/castor/e2e/origin"
)

// SessionCookie opens an Akamai-style cookie session on the master and refuses everything read outside it.
type SessionCookie struct{}

func (SessionCookie) Name() string { return "session-cookie" }

func (SessionCookie) Wrap(next http.Handler, p origin.Published) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("hdntl"); err == nil {
			next.ServeHTTP(w, r)
			return
		}
		if p.IsSegment(r) {
			http.Error(w, "session required", http.StatusForbidden)
			return
		}
		// The master is recognised by its body, so it opens the session whatever name it is served under.
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		if !strings.Contains(rec.Body.String(), "#EXT-X-STREAM-INF") {
			http.Error(w, "session required", http.StatusForbidden)
			return
		}
		maps.Copy(w.Header(), rec.Header())
		http.SetCookie(w, &http.Cookie{Name: "hdntl", Value: "exp=9999999999~acl=/*~hmac=e2e", Path: "/"})
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}
