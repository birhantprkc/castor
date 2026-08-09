// Package replay serves a single producer's stream over local HTTP, replaying
// it to every client from byte 0.
//
// The producer's output is spooled to disk and every HTTP connection replays
// it from the start through its own reader. This is what makes a renderer's
// HEAD-probe → short-GET → real-GET dance safe: the probe GET reads its own
// copy of the stream head and the real GET still starts at byte 0 with the
// container init and first keyframe intact. A live fan-out broadcaster (the
// previous design) hands the probe the only copy of the stream head, and the
// real GET then joins mid-stream at an arbitrary byte offset the renderer
// cannot decode.
//
// Delivery is not rate-limited: each connection is written as fast as the
// renderer reads it, and TCP backpressure holds it to the renderer's own
// playback rate. The producer runs ahead into the spool as fast as it
// encodes, so a late or reconnecting client can always be served from the
// start.
//
// Replaying from byte 0 is what a client gets when it asks for nothing else,
// and a reconnect that asks for nothing else therefore restarts the program.
// That is the cost of a viewer's pause: the socket stops draining, the write
// deadline severs the connection, and the next GET starts the film again.
//
// A client that asks to CONTINUE can be served from its offset instead, because
// the spool still holds every byte it had (see resume), and on a delivery whose
// responses take ranges that is what the pause costs: nothing. It is not what a
// pause costs everywhere, and the difference is the delivery's own headers
// rather than anything a client does. A delivery configured with
// Accept-Ranges: none has promised the renderer that no partial response will
// arrive, so every open-ended resume on it is refused and the program really
// does start over (see rangesDeclined). That is the configuration castor serves
// a renderer which cannot fetch for itself under, which makes the restart the
// ordinary outcome of a long pause rather than the exotic one, and it is why a
// severance is logged with what it cost (see severed).
//
// Nothing here advertises Accept-Ranges of its own: a resume is answered when a
// client asks for one, and no client is invited to seek into a stream whose
// length is not known yet.
package replay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stupside/castor/internal/cast/deliver/spool"
	"github.com/stupside/castor/internal/cast/watch"
)

const (
	// sendChunkSize is the per-connection read/write granularity.
	sendChunkSize = 32 * 1024

	// idleGrace is how long Wait keeps the server alive after the stream is
	// fully produced and the last client dropped mid-stream: long enough
	// for a renderer hiccup/reconnect, short enough not to hang the CLI.
	idleGrace = 30 * time.Second

	// DefaultWriteDeadline bounds ONE chunk write to one client, so a renderer that stops
	// reading cannot park a goroutine on a blocked socket for the rest of the process's
	// life. That is what used to happen, and it hung the cast outright: Wait needs the
	// client count to reach zero, and a goroutine wedged inside Write never decrements it.
	//
	// It is derived rather than picked: the longest silence any judgement castor makes
	// tolerates (watch.StallWindow, the bound on a producer that has gone quiet) plus one idle
	// grace, so severing is the LAST party to answer a quiet peer rather than the first.
	// Severing here is expensive, because every connection replays from byte 0 and a renderer
	// that reconnects restarts the film from the beginning.
	//
	// It IS reachable by a cast someone is watching, and saying so is the point of naming it
	// here. A viewer who pauses stops draining this socket while castor still has media for
	// them, and no verdict convicts that: from outside, a pause, a viewer who walked away and
	// a crashed renderer are the same absence of anything being taken, so how long a human
	// pause may be is this bound's to hold and nothing else's. Whether the renderer ever came
	// back for the rest is answered afterwards, from what it was handed (see Handed), rather
	// than guessed at while it is quiet.
	//
	// What a longer pause costs is the viewer's POSITION, and how much it costs is decided by
	// this delivery's own response headers rather than by anything here:
	//   - Where they take ranges, a reconnect that asks to continue is served from its offset
	//     instead of from byte 0 (see resume), and the pause costs nothing at all.
	//   - Where they declare Accept-Ranges: none, every open-ended resume is REFUSED (see
	//     rangesDeclined) and the film starts over from the beginning whatever the reconnect
	//     asks for. The declaration is the renderer's own (StreamHeaders) and castor may not
	//     contradict it, so the resume is unavailable exactly where it would be worth most: the
	//     one family that makes it (alongside DLNA.ORG_OP=00, which advertises no seek
	//     operations at all) is the family castor serves when the renderer cannot fetch for
	//     itself, i.e. the casts whose every byte castor produced. Whether that family can be
	//     told to accept ranges is a question about that header, not about this server.
	//
	// So a severance is logged with the numbers that account for it, including whether a resume
	// could be answered at all (see severed). A film that restarts itself with nothing in the
	// log to explain it is the worse half of this trade, and on a delivery that takes no ranges
	// the restart is the only outcome available.
	//
	// It is exported because it is the longest stretch castor lets any peer stay quiet for, so
	// anything reasoning about what a quiet renderer costs has to be stated in terms of it
	// instead of recomputing it: the recomputed version of this number landed one idle grace
	// away from the bound production holds, and called the difference the limit.
	DefaultWriteDeadline = watch.StallWindow + idleGrace
)

// Config is what the planner fills in.
type Config struct {
	LocalIP     string
	ContentType string
	Extension   string
	Headers     map[string]string

	// SpoolPath is where the producer's output is spooled. The caller owns
	// the file's directory lifecycle.
	SpoolPath string

	// WriteDeadline overrides how long one chunk write to one client may block before
	// the connection is severed. Zero uses DefaultWriteDeadline; tests set it small,
	// because the default is derived to be longer than any cast still worth serving.
	WriteDeadline time.Duration
}

// Server spools a producer's output and replays it to every HTTP client from
// the beginning.
type Server struct {
	cfg Config

	listener net.Listener
	server   *http.Server
	cancel   context.CancelFunc
	spool    *spool.Spool

	done chan struct{} // producer fully spooled

	mu             sync.Mutex
	active         int
	completed      bool // some client consumed the stream to EOF
	lastDisconnect time.Time
	// sent is the most bytes any ONE client was handed and lastFetch when a byte last moved
	// to one. They are what makes "the renderer accepted Play and never came for the bytes"
	// sayable: everything else this server tracks is satisfied by a cast nobody ever fetched,
	// so Wait reported one as delivered. See Handed.
	sent      int64
	lastFetch time.Time
}

// New binds to cfg.LocalIP on an ephemeral port and starts spooling producer
// in the background.
func New(cfg Config, producer io.Reader) (*Server, error) {
	if cfg.WriteDeadline <= 0 {
		cfg.WriteDeadline = DefaultWriteDeadline
	}
	sp, err := spool.New(cfg.SpoolPath)
	if err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", cfg.LocalIP+":0")
	if err != nil {
		sp.CloseWrite(nil)
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		cfg:            cfg,
		listener:       ln,
		cancel:         cancel,
		spool:          sp,
		done:           make(chan struct{}),
		lastDisconnect: time.Now(),
	}

	go func() {
		defer close(s.done)
		_, copyErr := io.Copy(sp, producer)
		sp.CloseWrite(copyErr)
		slog.Debug("stream fully spooled", "bytes", sp.Size(), "error", copyErr)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/stream"+cfg.Extension, func(w http.ResponseWriter, r *http.Request) {
		s.handleStream(ctx, w, r)
	})
	s.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() { _ = s.server.Serve(ln) }()
	return s, nil
}

// URL is the address the renderer should fetch.
func (s *Server) URL() *url.URL {
	return &url.URL{Scheme: "http", Host: s.listener.Addr().String(), Path: "/stream" + s.cfg.Extension}
}

// Close stops accepting connections and severs active ones.
// Produced is how many bytes the producer has written into the spool so far. It
// is what a caller waits on to know the producer is actually producing, rather
// than about to announce that it cannot.
func (s *Server) Produced() int64 { return s.spool.Size() }

// ProducerDone is closed once the producer's output has ended. The server reads
// that pipe, so it is the only party that sees the end of it; a caller waiting to
// find out whether a stream ever started needs this to tell "still starting" from
// "already over".
func (s *Server) ProducerDone() <-chan struct{} { return s.done }

// Handed is the most of this stream any ONE client was handed, and when a byte of it last
// moved. It is how much of the program the renderer can be said to have received, and it is
// what a judgement about a renderer rests on both while a cast runs (see watch.Consumer) and
// once it is over (see core.Undelivered).
//
// The MOST and not the sum: every connection replays from byte 0, so the longest single
// connection is the whole of what got through, while adding them would count the same head
// twice for every renderer that probes with a short GET before the real one.
//
// BYTES and not a count of requests, which is the fact that separates a renderer from a
// renderer's probe. A request is answered before a byte of it is written, and this server's
// clients arrive as a HEAD, then a short GET, then the real GET, so a count says "it fetched"
// about a renderer that asked and got nothing: the failure being reported, offered as evidence
// against itself.
//
// last moves only when a byte does, and is the zero time until one has, which is what lets a
// caller tell "has taken nothing yet" from "took something a long time ago" without this server
// guessing when the renderer was told about the URL. A connection ENDING is deliberately not a
// fetch: a probe that opened the stream, took nothing and closed would otherwise restart the
// recency clock, and a renderer probing on a cadence would never be quiet long enough to be
// named however little it ever took.
//
// It over-counts by whatever the kernel accepted and the renderer never read, which is a
// socket buffer at most and is the safe direction: the caller judges a cast on this figure
// being a small SHARE of what was produced, and over-counting can only excuse a delivery,
// never convict one.
func (s *Server) Handed() (int64, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent, s.lastFetch
}

func (s *Server) Close() error {
	s.cancel()
	return s.server.Close()
}

// Wait blocks until the stream has been fully produced AND delivered: a
// client has read it to EOF (movie over), or no client is left and none
// returned within the grace window, or ctx is cancelled. The producer
// finishing is explicitly NOT enough: it runs faster than playback, so the
// renderer is still mid-movie when the spool completes.
func (s *Server) Wait(ctx context.Context) error {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}

		select {
		case <-s.done:
		default:
			continue // still producing
		}

		s.mu.Lock()
		finished := s.active == 0 &&
			(s.completed || time.Since(s.lastDisconnect) > idleGrace)
		s.mu.Unlock()
		if finished {
			return nil
		}
	}
}

// served is the stretch of the stream one response carries, and the status that states it.
type served struct {
	// code is 200 (the whole stream from byte 0), 206 (the stretch below) or 416 (a start
	// past the end of a stream that has one).
	code int
	// start is the first byte of the stream this response carries, end the last one
	// inclusive, and end is -1 for "to the end of the stream, whenever the producer gets
	// there", which only a 200 ever is.
	start int64
	end   int64
	// total is the stream's full length, and -1 while the producer may still append to it.
	total int64
}

// resume works out which stretch of the stream answers this request, from the Range the client
// sent (empty when it sent none) and whether the producer has finished. The second return is
// why a resume that was asked for is not being honoured, for the log, and is empty otherwise.
//
// THE FAILURE THIS PREVENTS: a viewer pauses, the renderer stops draining the socket, and the
// blocked write is severed at the write deadline. Every connection replays from byte 0, so the
// renderer's next GET restarts the program from the beginning, silently, at whatever point the
// viewer had reached. The spool still holds every byte that renderer had, so the only thing
// standing between a reconnect and its position is whether this server answers the offset it
// asks for.
//
// A range is answered only where the answer can be TRUTHFUL, and the refusals carry the
// reasoning. The first of them is the one that decides most casts, and it is not about what can
// be served but about what was promised:
//   - Any range at all on a delivery whose own headers declare Accept-Ranges: none: REFUSED,
//     and this is the case a paused viewer actually meets, because that declaration is what the
//     family castor serves a non-self-fetching renderer under sends (see rangesDeclined). On
//     those casts the position is gone the moment the write deadline severs the socket, whatever
//     the reconnect asks for, and the refusal is logged rather than left to be inferred.
//   - No range at all, or bytes=0- : the ordinary replay, 200 from byte 0. It is what the
//     HEAD-probe, short-GET, real-GET dance depends on, and a client asking for the whole thing
//     from byte 0 is asking for exactly those bytes.
//   - The client named its own last byte (bytes=N-M): answerable at any time, including while
//     the producer is still running, because the response states a stretch the CLIENT chose and
//     invents no length of its own.
//   - bytes=N- once the producer has finished: the length is final, so the response can state
//     it.
//   - bytes=N- while the producer is still running: REFUSED, and this is the hole in the fix
//     rather than an oversight. A 206 must name a last byte, and the only candidates are a
//     number nobody knows yet or the bytes produced so far. The second is worse than the
//     restart it would avoid: a client told the film ends where the encoder happens to have
//     reached stops mid-title with no error in any log, while a client replayed from byte 0 is
//     at least visibly at the beginning.
func (s *Server) resume(rangeHeader string) (served, string) {
	whole := served{code: http.StatusOK, start: 0, end: -1, total: -1}
	if rangeHeader == "" {
		return whole, ""
	}
	start, end, ok := byteRange(rangeHeader)
	if !ok {
		return whole, "the Range header is not a single bytes=first-last range"
	}
	if start == 0 && end < 0 {
		// Asking for the whole thing from byte 0 is the ordinary replay: the client is asking
		// for exactly the bytes it is about to be given, so there is no position at stake here
		// to honour or to refuse.
		return whole, ""
	}
	// A refusal is only worth a line when it costs the client its POSITION. A client that asked
	// from byte 0 and is served from byte 0 has lost nothing whatever shape it asked in, and the
	// GET line already records what every client asked for, so warning about those would bury the
	// one refusal that matters under the probes.
	refuse := func(why string) (served, string) {
		if start == 0 {
			return whole, ""
		}
		return whole, why
	}
	if s.rangesDeclined() {
		return refuse("this delivery's own response headers declare Accept-Ranges: none")
	}
	total, final := s.spooled()
	unsatisfiable := served{code: http.StatusRequestedRangeNotSatisfiable, total: total}
	switch {
	case end >= 0:
		// The client named its own last byte, so there is nothing to invent. A final length
		// still bounds it: a client is entitled to ask past the end and to be told where it is.
		if !final {
			return served{code: http.StatusPartialContent, start: start, end: end, total: -1}, ""
		}
		if start >= total {
			return unsatisfiable, ""
		}
		return served{code: http.StatusPartialContent, start: start, end: min(end, total-1), total: total}, ""
	case !final:
		return refuse("the producer is still running, so no response can state where this stream ends")
	case start >= total:
		return unsatisfiable, ""
	default:
		return served{code: http.StatusPartialContent, start: start, end: total - 1, total: total}, ""
	}
}

// byteRange parses the one form a resume takes: a single "bytes=first-last", with the last byte
// optional. Anything else (a set of ranges, a suffix range, a unit this server does not serve)
// is not a resume, and is answered with the whole stream under a 200, which is what a client
// that gets a 200 to a range request is required to accept.
func byteRange(header string) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	first, last, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	if strings.TrimSpace(last) == "" {
		return start, -1, true
	}
	end, err = strconv.ParseInt(strings.TrimSpace(last), 10, 64)
	if err != nil || end < start {
		return 0, 0, false
	}
	return start, end, true
}

// rangesDeclined reports whether the responses this delivery is configured to send declare
// that they take no ranges.
//
// The renderer's protocol headers are the DEVICE's statement about what it may ask for
// (StreamHeaders), so answering a 206 on a response carrying Accept-Ranges: none would be
// castor contradicting its own promise: a firmware told the stream is unseekable and then handed
// a partial response is being asked to handle the one shape it was told would not arrive. The
// refusal is logged with this reason named, which is what makes the header, and not this
// server, the thing to change if a family turns out to want resumes.
func (s *Server) rangesDeclined() bool {
	for k, v := range s.cfg.Headers {
		if http.CanonicalHeaderKey(k) == "Accept-Ranges" {
			return strings.EqualFold(strings.TrimSpace(v), "none")
		}
	}
	return false
}

// spooled is how many bytes the producer has written and whether that figure is final.
//
// Final is read from the producer's own end and never from the size holding still: a size that
// has not moved is a slow encoder, and a range response that stated it as the stream's length
// would tell a client the film ends where the encoder happened to have reached.
func (s *Server) spooled() (int64, bool) {
	select {
	case <-s.done:
		return s.spool.Size(), true
	default:
		return s.spool.Size(), false
	}
}

// severed says why one client stopped being written to, and the distinction it draws is the
// whole point of it.
//
// A client that closed the socket is ordinary: a renderer that was stopped, or a probe GET that
// got what it came for. A client that stopped DRAINING and was cut off at the write deadline is
// a viewer whose position castor is about to destroy, because unless the reconnect asks to
// resume the next GET replays from byte 0 and the film starts over with nothing anywhere to
// explain it. So this states the numbers that account for the restart at the only moment
// anything holds them: how long the client had taken nothing, how much of the stream it had, and
// whether a resume could be answered at all if it asks for one.
//
// resumable is the pessimistic reading of the two, and deliberately so: it answers for the
// open-ended reconnect a renderer coming back from a pause actually makes. A client that names
// its own last byte can be served at any point in a cast (see resume), so a false here means
// "not resumable by asking for the rest", never "nothing can be served".
func (s *Server) severed(ctx context.Context, r *http.Request, err error, held int64, stalled time.Duration) {
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		slog.InfoContext(ctx, "stream client disconnected", "from", r.RemoteAddr, "bytes_sent", held, "error", err)
		return
	}
	_, final := s.spooled()
	slog.WarnContext(ctx, "severed a client that stopped draining the stream; unless it reconnects with a Range it restarts the program from the beginning",
		"from", r.RemoteAddr,
		"user_agent", r.UserAgent(),
		"stalled_for", stalled.Round(time.Millisecond),
		"write_deadline", s.cfg.WriteDeadline,
		"bytes_sent", held,
		"resumable", final && !s.rangesDeclined(),
	)
}

// handleStream serves one client the stretch of the spool it asked for, which is the whole of
// it from byte 0 unless the client asked to continue from an offset. srvCtx ends the stream on
// server shutdown; the request context ends it on client disconnect.
func (s *Server) handleStream(srvCtx context.Context, w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(srvCtx, cancel)
	defer stop()

	w.Header().Set("Content-Type", s.cfg.ContentType)
	for k, v := range s.cfg.Headers {
		w.Header().Set(k, v)
	}
	if r.Method == http.MethodHead {
		slog.InfoContext(ctx, "stream HEAD", "from", r.RemoteAddr, "user_agent", r.UserAgent())
		w.WriteHeader(http.StatusOK)
		return
	}

	asked := r.Header.Get("Range")
	sv, refused := s.resume(asked)
	slog.InfoContext(ctx, "stream GET",
		"from", r.RemoteAddr,
		"user_agent", r.UserAgent(),
		"range", asked,
		"status", sv.code,
		"first_byte", sv.start,
	)
	if refused != "" {
		// WARN because of what it costs: the client asked for its position and is getting the
		// beginning of the film instead, which is the same restart the write deadline causes and
		// is just as invisible from the outside without this line.
		slog.WarnContext(ctx, "a client asked to continue the stream and is being replayed from the beginning instead",
			"from", r.RemoteAddr, "user_agent", r.UserAgent(), "range", asked, "reason", refused)
	}
	if sv.code == http.StatusRequestedRangeNotSatisfiable {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", sv.total))
		http.Error(w, "requested range not satisfiable", sv.code)
		return
	}

	tail, err := s.spool.TailAt(ctx, sv.start)
	if err != nil {
		http.Error(w, "stream unavailable", http.StatusServiceUnavailable)
		return
	}
	defer tail.Close()

	s.mu.Lock()
	s.active++
	s.mu.Unlock()
	reachedEOF := false
	// The end of a connection is not a fetch, and touching the recency clock here is how a
	// renderer that never took a byte looked like one that had just been taking them: a probe
	// GET reaches this defer having moved nothing, so a renderer probing on any cadence stayed
	// permanently inside the window the one verdict about a renderer waits out. What a client
	// took is recorded where bytes really move (see Handed); what ends here is the count of
	// clients and the idle clock Wait reads.
	defer func() {
		s.mu.Lock()
		s.active--
		s.completed = s.completed || reachedEOF
		s.lastDisconnect = time.Now()
		s.mu.Unlock()
	}()

	// A partial response states the stretch it carries. Content-Length is set from that
	// stretch and not left to chunked encoding, because a resuming client uses it to know the
	// response is the continuation it asked for; a complete-length of * is what an unfinished
	// producer honestly has to say (see resume).
	if sv.code == http.StatusPartialContent {
		if sv.total >= 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", sv.start, sv.end, sv.total))
		} else {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/*", sv.start, sv.end))
		}
		w.Header().Set("Content-Length", strconv.FormatInt(sv.end-sv.start+1, 10))
	}
	w.WriteHeader(sv.code)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	// remaining is how many bytes this response still owes, and -1 when it owes the rest of the
	// stream however long that turns out to be. A partial response may not over-run the length
	// it declared: Go reports that as "wrote more than the declared Content-Length" and the
	// client is handed a body that disagrees with its own header.
	remaining := int64(-1)
	if sv.end >= 0 {
		remaining = sv.end - sv.start + 1
	}

	// The deadline is re-armed per chunk rather than set once for the response, because
	// the thing being bounded is one write to a client that has stopped reading, not how
	// long a renderer may watch a film.
	control := http.NewResponseController(w)
	buf := make([]byte, sendChunkSize)
	var sent int64
	// Since the last byte this client actually took, which is what makes a severance
	// attributable: the deadline says how long a write was allowed to block, and this says how
	// long the client had really been taking nothing when it was cut off.
	lastWrite := time.Now()
	for {
		want := buf
		if remaining >= 0 {
			want = buf[:min(int64(len(buf)), remaining)]
		}
		n, readErr := tail.Read(want)
		if n > 0 {
			if err := control.SetWriteDeadline(time.Now().Add(s.cfg.WriteDeadline)); err != nil {
				slog.WarnContext(ctx, "stream write deadline unavailable, a client that stops reading will park this goroutine", "error", err)
			}
			if _, err := w.Write(want[:n]); err != nil {
				s.severed(ctx, r, err, sv.start+sent, time.Since(lastWrite))
				return
			}
			sent += int64(n)
			lastWrite = time.Now()
			if remaining >= 0 {
				remaining -= int64(n)
			}
			s.mu.Lock()
			s.lastFetch = lastWrite
			// A resumed connection is credited with the prefix it said it already had: asking
			// for byte N is the client stating it holds 0 to N-1, and this figure is about how
			// much of the PROGRAM reached the renderer rather than about one socket. Counting
			// only this connection's bytes would report a renderer that resumed at the half-way
			// mark and watched to the end as having taken half the film, and the completeness
			// statement would then convict it (see core.Undelivered).
			s.sent = max(s.sent, sv.start+sent)
			s.mu.Unlock()
			if flusher != nil {
				flusher.Flush()
			}
		}
		if remaining == 0 {
			// The stretch the client asked for is delivered. It is the end of the STREAM only
			// where it ended on the stream's own last byte, which is the difference between a
			// film that was watched to the end and a client that fetched one slice of it.
			reachedEOF = sv.total >= 0 && sv.end == sv.total-1
			slog.InfoContext(ctx, "stream range delivered",
				"from", r.RemoteAddr, "bytes_sent", sent, "first_byte", sv.start, "last_byte", sv.end)
			return
		}
		if readErr != nil {
			if readErr == io.EOF {
				reachedEOF = true
				slog.InfoContext(ctx, "stream fully delivered", "from", r.RemoteAddr, "bytes_sent", sent)
			} else {
				slog.InfoContext(ctx, "stream read ended", "from", r.RemoteAddr, "bytes_sent", sent, "error", readErr)
			}
			return
		}
	}
}
