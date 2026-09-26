package faults

import (
	"maps"
	"net/http"
	"net/http/httptest"

	"github.com/stupside/castor/e2e/origin"
)

// IgnoresRange answers every segment whole from its first byte, as a host that does not serve byte ranges.
type IgnoresRange struct{}

func (IgnoresRange) Name() string { return "ignores-range" }

func (IgnoresRange) Wrap(next http.Handler, p origin.Published) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !p.IsSegment(r) {
			next.ServeHTTP(w, r)
			return
		}
		whole := r.Clone(r.Context())
		whole.Header.Del("Range")
		whole.Header.Del("If-Range")
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, whole)
		maps.Copy(w.Header(), rec.Header())
		w.Header().Del("Accept-Ranges")
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}
