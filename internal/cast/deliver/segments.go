package deliver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stupside/castor/internal/cast/container"
)

type segmentsConfig struct {
	listeners Listeners
	dir       string            // Directory ffmpeg writes playlist and segments into.
	playlist  string            // Media playlist filename (container.Tuning.Output).
	headers   map[string]string // Response headers (device.StreamHeaders).
	idleGrace time.Duration
}

// Segments serves dir over HTTP, tracks liveness for Wait, deletes behind live edge.
type Segments struct {
	cfg      segmentsConfig
	listener net.Listener
	server   *http.Server
	playlist string

	drained chan struct{}
	reader  sync.WaitGroup

	mu          sync.Mutex
	lastRequest time.Time // When renderer last requested anything (seeded at New).
	served      int       // Artifacts handed over (measure of renderer fetch, not bytes).
}

// OpenSegments serves a live HLS directory; no byte pacing, the client self-paces.
func OpenSegments(ctx context.Context, o Opening) (*Segments, error) {
	srv, err := openSegments(ctx, segmentsConfig{
		listeners: o.Listeners,
		dir:       o.Dir,
		playlist:  o.Format.Tuning.Output,
		headers:   o.Headers,
		idleGrace: o.IdleGrace,
	}, o.Out)
	if err != nil {
		return nil, fmt.Errorf("starting HLS server: %w", err)
	}
	return srv, nil
}

func openSegments(ctx context.Context, cfg segmentsConfig, producer io.Reader) (*Segments, error) {
	ln, err := cfg.listeners.Listen(ctx)
	if err != nil {
		return nil, err
	}

	s := &Segments{
		cfg:         cfg,
		listener:    ln,
		playlist:    filepath.Join(cfg.dir, cfg.playlist),
		drained:     make(chan struct{}),
		lastRequest: time.Now(),
	}
	s.reader.Go(func() {
		// Drain to EOF (unread pipe stops ffmpeg once kernel buffer fills).
		defer close(s.drained)
		_, _ = io.Copy(io.Discard, producer)
	})

	files := http.FileServer(http.Dir(cfg.dir))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.touch()
		slog.InfoContext(r.Context(), "hls request", "from", r.RemoteAddr, "path", r.URL.Path)
		// Set renderer headers first, then artifact's own type.
		for k, v := range s.cfg.headers {
			w.Header().Set(k, v)
		}
		// Go doesn't register .m3u8/.m4s; set type before ServeContent sniffs.
		if ct, ok := container.HLSArtifactContentType(r.URL.Path); ok {
			w.Header().Set("Content-Type", ct)
		}
		// Count from answer, not request (404 on window-rolled segments shouldn't count).
		answer := &answered{ResponseWriter: w}
		files.ServeHTTP(answer, r)
		if r.Method == http.MethodGet && answer.carriedBytes() && s.carriesMedia(r.URL.Path) {
			s.handedOver()
		}
	})
	s.server = Serve(ctx, ln, mux)
	return s, nil
}

// URL is the media-playlist address the device should play.
func (s *Segments) URL() *url.URL {
	return &url.URL{Scheme: "http", Host: s.listener.Addr().String(), Path: "/" + s.cfg.playlist}
}

// Served returns artifacts handed over (zero = URL accepted but no bytes fetched).
func (s *Segments) Served() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served
}

// Drained is closed once encoder output reaches EOF.
func (s *Segments) Drained() <-chan struct{} { return s.drained }

func (s *Segments) Artifact() Artifact {
	// No patience (zero-byte m3u8 is unparseable).
	return Artifact{Subject: "the HLS playlist", Landed: written(s.playlist)}
}

// touch restarts idle grace on any request (playlist refresh counts as watching).
func (s *Segments) touch() {
	s.mu.Lock()
	s.lastRequest = time.Now()
	s.mu.Unlock()
}

func (s *Segments) handedOver() {
	s.mu.Lock()
	s.served++
	s.mu.Unlock()
}

// carriesMedia returns true for program (anything but playlist) to avoid silent count breakage.
func (s *Segments) carriesMedia(p string) bool {
	name := strings.TrimPrefix(path.Clean("/"+p), "/")
	return name != "" && name != s.cfg.playlist
}

// answered tracks response status for Served count (ResponseWriter doesn't report it).
type answered struct {
	http.ResponseWriter
	status int
}

func (a *answered) WriteHeader(code int) {
	if a.status == 0 {
		// First status is the one sent; second WriteHeader never reaches wire.
		a.status = code
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *answered) Write(b []byte) (int, error) {
	if a.status == 0 {
		// net/http's implicit 200
		a.status = http.StatusOK
	}
	return a.ResponseWriter.Write(b)
}

// ReadFrom keeps file server's ReaderFrom (sendfile, not userspace copy).
func (a *answered) ReadFrom(r io.Reader) (int64, error) {
	if a.status == 0 {
		a.status = http.StatusOK
	}
	return io.Copy(a.ResponseWriter, r)
}

// Unwrap lets http.ResponseController reach the connection's own flush and deadlines.
func (a *answered) Unwrap() http.ResponseWriter { return a.ResponseWriter }

// carriedBytes reports successful response (2xx); 404 and 416 hand over nothing.
func (a *answered) carriedBytes() bool { return a.status >= 200 && a.status < 300 }

// Wait blocks until stream is produced/drained or ctx cancelled (live source ends on Ctrl+C).
func (s *Segments) Wait(ctx context.Context) error {
	// Wait for producer EOF first; idleness check needs the clock.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.drained:
	}

	tick := time.NewTicker(SettleInterval)
	defer tick.Stop()
	for {
		s.mu.Lock()
		idle := time.Since(s.lastRequest) > s.cfg.idleGrace
		s.mu.Unlock()
		if idle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// Close stops HTTP server and joins encoder-output-draining goroutine.
func (s *Segments) Close() error {
	err := s.server.Close()
	s.reader.Wait()
	return err
}

// written reports file size on disk (zero-byte m3u8 is unparseable).
func written(path string) func() int64 {
	return func() int64 {
		info, err := os.Stat(path)
		if err != nil {
			return 0
		}
		return info.Size()
	}
}
