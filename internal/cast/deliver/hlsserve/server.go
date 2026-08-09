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

	"github.com/stupside/castor/internal/media"
)

// defaultIdleGrace is how long to keep serving after the producer finished and
// the client went quiet, so the tail segments still get fetched.
const defaultIdleGrace = 30 * time.Second

// pollInterval is how often Wait re-reads whether this delivery still has anything to do. It
// is named because it bounds what the grace can MEAN: a grace shorter than it is not a shorter
// grace, it is one nobody looks inside.
const pollInterval = 500 * time.Millisecond

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
	// lastRequest is when the renderer last asked for anything at all, the playlist
	// included, and it is seeded at New so the idle grace has somewhere to start from. A
	// renderer refreshing a playlist it is playing from is not an idle one.
	lastRequest time.Time
	// served is how many artifacts other than the playlist this delivery handed over, which
	// is the only thing here that can say whether any of the program reached the renderer:
	// producerDone is satisfied by an encoder that ran the title to its end whatever the
	// renderer did, and the seeded lastRequest answers that a renderer which never arrived
	// had just been here. Between them, a cast that accepted the URL and never fetched a
	// segment was reported as delivered and castor exited 0 having cast nothing.
	served int
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
		// Go doesn't register .m3u8/.m4s, so set the type before ServeContent sniffs. The
		// registry answers, not a table here: this server used to spell the playlist's type
		// differently from the way the renderer was told to expect it at Play.
		if ct, ok := media.HLSArtifactTypes[strings.ToLower(path.Ext(r.URL.Path))]; ok {
			w.Header().Set("Content-Type", ct)
		}
		// Counted after the answer and from the answer, because what this delivery has to be
		// able to state is what it HANDED OVER: a renderer that has fallen behind the rolling
		// window asks for a segment the muxer already deleted and is answered 404, and a count
		// of requests would report a cast that handed the renderer nothing but errors as one
		// that delivered.
		answer := &answered{ResponseWriter: w}
		files.ServeHTTP(answer, r)
		if r.Method == http.MethodGet && answer.carriedBytes() && s.carriesMedia(r.URL.Path) {
			s.handedOver()
		}
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

// Served is how many artifacts of the program this delivery handed over, which is what a
// cast's completeness statement is made from: zero is a renderer that accepted the URL and
// never came for a byte of what was produced for it (see core's segmented opener).
//
// It counts ARTIFACTS rather than bytes, and nothing here reports a share, because a share of
// a rolling window is meaningless: this muxer deletes behind its live edge, so a renderer that
// fetched every segment it was ever offered has still taken a fraction of the bytes the
// encoder wrote. Zero is the one figure a deleting window cannot excuse.
//
// The playlist is not one of them: it carries no media, and a renderer that polls it while
// asking for no segment is exactly what this delivery hides best, since every playlist GET
// 200s and every other party sees a cast somebody is watching. The initialisation
// segment IS counted along with the fragments, which errs towards acquitting a renderer that
// took the decoder configuration and then refused the picture. That is the direction a
// mechanism unable to state a share has to err in, and the failure this exists to name is a
// renderer that took NOTHING.
//
// A HEAD is deliberately not a fetch, for the reason the other delivery gives (see replay's
// Handed): a renderer that probes the URL and never gets the program is the exact failure
// being reported, and counting its probe would answer that it was watching.
func (s *Server) Served() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served
}

// touch restarts the idle grace, on any request at all: a renderer refreshing the playlist is
// watching the cast even while it is between segments.
func (s *Server) touch() {
	s.mu.Lock()
	s.lastRequest = time.Now()
	s.mu.Unlock()
}

// handedOver records that a piece of the program went out to the renderer.
func (s *Server) handedOver() {
	s.mu.Lock()
	s.served++
	s.mu.Unlock()
}

// carriesMedia reports whether a path out of this directory is program rather than metadata.
// It is stated as "anything but the playlist" so that the muxer's own choices cannot silently
// stop the count: a segment extension this server has never seen still counts as media, which
// can only ever excuse a delivery, while a rule naming today's extension would convict every
// cast the day the muxer wrote something else.
func (s *Server) carriesMedia(p string) bool {
	name := strings.TrimPrefix(path.Clean("/"+p), "/")
	return name != "" && name != s.cfg.Playlist
}

// answered is the status one response went out with, which http.ResponseWriter does not
// otherwise report back to the handler. The body is written straight through: this delivery
// counts what it handed over and not how much of it, because bytes off a deleting window
// answer no question anybody can ask (see Served).
type answered struct {
	http.ResponseWriter
	status int
}

func (a *answered) WriteHeader(code int) {
	if a.status == 0 {
		// The first status is the one that went out, which is net/http's own rule: a second
		// WriteHeader never reaches the wire, so recording it would report a status no renderer
		// was ever answered with.
		a.status = code
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *answered) Write(b []byte) (int, error) {
	if a.status == 0 {
		// net/http's own implicit 200: a handler that writes a body without stating a status
		// has stated this one.
		a.status = http.StatusOK
	}
	return a.ResponseWriter.Write(b)
}

// carriedBytes reports that this response handed the renderer what it asked for. A 404 (the
// segment the window rolled off) and a 416 (a range past the end) hand over nothing, and a
// renderer that only ever gets those has been served no program at all.
func (a *answered) carriedBytes() bool { return a.status >= 200 && a.status < 300 }

// Wait blocks until the stream is produced and drained, or ctx is cancelled. A
// live source never finishes producing, so it ends on ctx (Ctrl+C).
//
// Draining is measured from the last request, and the seed at New is what lets a renderer
// finish a title: the wait stays open for a full grace past the encoder's exit, which is
// exactly when the tail segments are still being fetched.
//
// It reports that the delivery RAN ITS COURSE, never that a renderer took it. Whether any of
// the program reached anybody is stated once, at the end of the cast, from the count this
// server keeps (see Served): making it here as well would be one question answered by two
// parties who can disagree about it, on the one delivery where nobody would notice.
func (s *Server) Wait(ctx context.Context) error {
	tick := time.NewTicker(pollInterval)
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
