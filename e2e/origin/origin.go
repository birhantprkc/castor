// Package origin stands up a test stream from a case's spec and records who fetched it.
// What varies (codecs, colour, packaging, how it is served) is a strategy the composition root lists in a Catalog.
package origin

import (
	"cmp"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Request is one fetch the origin answered.
type Request struct {
	Path      string
	UserAgent string
	Referer   string
	Status    int
}

type Origin struct {
	URL    string
	Stream Stream
	// Live is whether a behaviour serves the stream as a live edge.
	Live bool

	mu       sync.Mutex
	requests []Request
}

// Requests returns every fetch so far, in arrival order.
func (o *Origin) Requests() []Request {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.requests)
}

// Start encodes s with ffmpeg and serves it through behaviours, outermost first, until the test ends.
func Start(t *testing.T, ffmpeg string, s Stream, behaviours []Behaviour) *Origin {
	t.Helper()
	dir := t.TempDir()
	out := s.Packager.Package(dir, s.layout())
	for name, body := range out.Files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatalf("packaging the origin: %v", err)
		}
	}
	if log, err := exec.CommandContext(t.Context(), ffmpeg, slices.Concat(s.encodeArgs(), out.Args)...).CombinedOutput(); err != nil {
		t.Fatalf("encoding the origin: %v\n%s", err, log)
	}
	for _, step := range out.Then {
		if log, err := exec.CommandContext(t.Context(), ffmpeg, step...).CombinedOutput(); err != nil {
			t.Fatalf("packaging the origin: %v\n%s", err, log)
		}
	}

	types := maps.Clone(out.Types)
	types[out.SegmentExt] = cmp.Or(s.Segments.ServedAs, types[out.SegmentExt])
	files := http.FileServer(http.Dir(dir))
	entryPath, _, _ := strings.Cut(s.Entry.Path, "?")
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Entry.Path != "" && r.URL.Path == "/"+entryPath {
			renamed := r.Clone(r.Context())
			renamed.URL.Path = "/" + out.Entry
			w.Header().Set("Content-Type", s.Entry.ServedAs)
			files.ServeHTTP(w, renamed)
			return
		}
		w.Header().Set("Content-Type", cmp.Or(types[path.Ext(r.URL.Path)], "application/octet-stream"))
		files.ServeHTTP(w, r)
	})
	closing := make(chan struct{})
	published := Published{SegmentExt: out.SegmentExt, Since: time.Now(), Closing: closing}
	for _, b := range slices.Backward(behaviours) {
		handler = b.Wrap(handler, published)
	}

	o := &Origin{Stream: s, Live: slices.ContainsFunc(behaviours, func(b Behaviour) bool {
		_, edge := b.(Edge)
		return edge
	})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		handler.ServeHTTP(rec, r)
		o.mu.Lock()
		o.requests = append(o.requests, Request{Path: r.URL.Path, UserAgent: r.UserAgent(), Referer: r.Referer(), Status: rec.status})
		o.mu.Unlock()
	}))
	t.Cleanup(func() { close(closing); server.Close() })
	o.URL = server.URL + "/" + cmp.Or(s.Entry.Path, out.Entry)
	return o
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Flush keeps a behaviour that streams slowly able to push each chunk.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
