package access

import (
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/stupside/castor/e2e/origin"
)

// RotatesCookie is a CDN that renews its session cookie on every playlist it serves and refuses segments read with one more than two renewals old.
type RotatesCookie struct{}

func (RotatesCookie) Name() string { return "rotates-cookie" }

// kept is how many renewals back a cookie still opens a segment.
const kept = 2

func (RotatesCookie) Wrap(next http.Handler, p origin.Published) http.Handler {
	var mu sync.Mutex
	issued := 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.IsSegment(r) {
			c, err := r.Cookie("edge")
			if err != nil {
				http.Error(w, "stale edge session", http.StatusForbidden)
				return
			}
			n, _ := strconv.Atoi(strings.TrimPrefix(c.Value, "gen-"))
			mu.Lock()
			current := issued
			mu.Unlock()
			if n < current-kept {
				http.Error(w, "stale edge session", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			mu.Lock()
			issued++
			http.SetCookie(w, &http.Cookie{Name: "edge", Value: "gen-" + strconv.Itoa(issued), Path: "/"})
			mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}
