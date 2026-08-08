// Package hlsserve serves a live HLS directory (playlist + rolling fMP4 segments)
// over local HTTP. There is no byte pacing: an HLS client self-paces, and ffmpeg
// bounds the on-disk window. URL/Wait/Close match the replay server's shape so
// the cast path composes over either.
package hlsserve

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// defaultIdleGrace is how long to keep serving after the producer finished and
// the client went quiet, so the tail segments still get fetched.
const defaultIdleGrace = 30 * time.Second

// Config is what the caller fills in.
type Config struct {
	LocalIP string // address to bind the HTTP listener
	Dir     string // directory ffmpeg writes the playlist and segments into
	// Playlist is the media playlist filename within Dir (media.HLSPlaylistName).
	Playlist string
	// Headers are what a response fronting this stream must say, as the renderer asked
	// (device.StreamHeaders). A segmented delivery carries them for the same reason a
	// streamed one does, and dropping them here was invisible: the playlist 200s, every
	// segment 200s, and a family that only fetches what its transfer-mode header
	// announces simply never comes back for the second segment.
	Headers map[string]string
	// IdleGrace overrides how long Wait keeps serving after the producer is done
	// and the client goes quiet. Zero uses defaultIdleGrace; tests set it small.
	IdleGrace time.Duration
}

// Server serves Config.Dir over HTTP and tracks liveness so Wait can end the
// cast once the stream is fully produced and drained.
type Server struct {
	cfg      Config
	listener net.Listener
	server   *http.Server

	mu           sync.Mutex
	producerDone bool
	lastRequest  time.Time
	// requests is how many times a renderer has fetched something out of the directory.
	// Without it, a cast nobody ever fetched satisfies every other condition here and
	// Wait reports it as delivered.
	requests int
}

// New binds an ephemeral port on cfg.LocalIP and starts serving cfg.Dir.
func New(cfg Config) (*Server, error) {
	if cfg.IdleGrace <= 0 {
		cfg.IdleGrace = defaultIdleGrace
	}
	ln, err := net.Listen("tcp", cfg.LocalIP+":0")
	if err != nil {
		return nil, err
	}

	s := &Server{cfg: cfg, listener: ln, lastRequest: time.Now()}

	files := http.FileServer(http.Dir(cfg.Dir))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.touch()
		slog.InfoContext(r.Context(), "hls request", "from", r.RemoteAddr, "path", r.URL.Path)
		// The renderer's headers first, then the artifact's own type: what a .m3u8 or a
		// .m4s IS cannot be overridden by a device's transfer-mode preferences.
		for k, v := range s.cfg.Headers {
			w.Header().Set(k, v)
		}
		// Go doesn't register .m3u8/.m4s, so set the type before ServeContent sniffs.
		if ct := contentTypeFor(r.URL.Path); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		files.ServeHTTP(w, r)
	})
	s.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() { _ = s.server.Serve(ln) }()
	return s, nil
}

// URL is the media-playlist address the device should play.
func (s *Server) URL() *url.URL {
	return &url.URL{Scheme: "http", Host: s.listener.Addr().String(), Path: "/" + s.cfg.Playlist}
}

// ProducerEnded records that the encoder has exited, letting Wait return once the client
// drains the tail.
//
// It is named for the statement it makes rather than for the state it sets, because the
// other server behind the same delivery port spells ProducerDone as a channel a caller
// waits ON. Two types reachable through one port must not disagree about whether a
// method name asks a question or answers one.
func (s *Server) ProducerEnded() {
	s.mu.Lock()
	s.producerDone = true
	s.mu.Unlock()
}

// Fetched is how many times a renderer has come for something in this directory and when
// it last did. Segment GETs are transient, so a live connection count here is nearly
// always zero and could never answer whether anybody is watching; the count of requests
// can.
func (s *Server) Fetched() (int, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requests == 0 {
		// lastRequest is seeded at New so the idle grace has somewhere to start from.
		// Reporting it as a fetch would answer that a renderer which never arrived had
		// just been here.
		return 0, time.Time{}
	}
	return s.requests, s.lastRequest
}

func (s *Server) touch() {
	s.mu.Lock()
	s.lastRequest = time.Now()
	s.requests++
	s.mu.Unlock()
}

// Wait blocks until the stream is produced and drained, or ctx is cancelled. A
// live source never finishes producing, so it ends on ctx (Ctrl+C).
func (s *Server) Wait(ctx context.Context) error {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		s.mu.Lock()
		finished := s.producerDone && time.Since(s.lastRequest) > s.cfg.IdleGrace
		s.mu.Unlock()
		if finished {
			return nil
		}
	}
}

// Close stops the HTTP server.
func (s *Server) Close() error {
	return s.server.Close()
}

// contentTypeFor returns the MIME type for an HLS artifact by extension, or ""
// to let the file server decide.
func contentTypeFor(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".m4s":
		return "video/iso.segment"
	case ".mp4":
		return "video/mp4"
	default:
		return ""
	}
}
