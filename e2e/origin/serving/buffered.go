package serving

import (
	"maps"
	"net/http"
	"net/http/httptest"
)

// Buffered serves the request into memory and forwards its headers, so a behaviour can pace the body itself.
func Buffered(next http.Handler, w http.ResponseWriter, r *http.Request) []byte {
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, r)
	maps.Copy(w.Header(), rec.Header())
	w.Header().Del("Content-Length")
	w.WriteHeader(rec.Code)
	return rec.Body.Bytes()
}
