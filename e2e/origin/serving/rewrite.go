// Package serving is what origin behaviours share: rewriting a document, buffering a body, numbering arrivals, reading playlists.
package serving

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
)

// Rewrite serves next's whole answer through edit, which reports whether it changed the body; an error answers 500.
// A rewritten body has no ranges or validators the file on disk could answer for, so the inner read is unranged,
// unconditional and a GET, and a HEAD is answered with the edited length.
func Rewrite(next http.Handler, w http.ResponseWriter, r *http.Request, edit func([]byte) ([]byte, bool, error)) {
	whole := r.Clone(r.Context())
	whole.Method = http.MethodGet
	for _, h := range []string{"Range", "If-Range", "If-Modified-Since", "If-None-Match"} {
		whole.Header.Del(h)
	}
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, whole)
	body, changed := rec.Body.Bytes(), false
	if rec.Code == http.StatusOK {
		edited, ok, err := edit(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ok {
			body, changed = edited, true
		}
	}
	maps.Copy(w.Header(), rec.Header())
	if changed {
		for _, h := range []string{"Content-Range", "Accept-Ranges", "Last-Modified", "Etag"} {
			w.Header().Del(h)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.WriteHeader(rec.Code)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// RewritesDocs rewrites every response whose path ends in ext and passes everything else to next.
func RewritesDocs(ext string, next http.Handler, edit func(doc string) (string, bool, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path.Ext(r.URL.Path) != ext {
			next.ServeHTTP(w, r)
			return
		}
		Rewrite(next, w, r, func(body []byte) ([]byte, bool, error) {
			doc, changed, err := edit(string(body))
			return []byte(doc), changed, err
		})
	})
}

// RewritesPlaylists edits HLS playlists; edit declines with false to replay the playlist untouched.
func RewritesPlaylists(next http.Handler, edit func(playlist string) (string, bool)) http.Handler {
	return RewritesDocs(".m3u8", next, func(doc string) (string, bool, error) {
		edited, changed := edit(doc)
		return edited, changed, nil
	})
}

// RewritesMPD edits DASH presentations.
func RewritesMPD(next http.Handler, edit func(mpd string) (string, error)) http.Handler {
	return RewritesDocs(".mpd", next, func(doc string) (string, bool, error) {
		edited, err := edit(doc)
		return edited, true, err
	})
}
