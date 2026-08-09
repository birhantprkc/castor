package e2e

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

// A hostile origin: a real HTTP server, over real generated media, misbehaving in exactly
// one of the ways origins misbehave in the field.
//
// It exists because every rule in internal/cast/watch is verified against fakes and
// hand-built Health values, and that class of verification cannot answer the question those
// rules exist for: does the shipping wiring reach them when an origin does this. A rule
// keyed on a fact no monitor in its window fills reads that fact's zero value on every real
// cast, so it fails nothing while reading as coverage, and that is not hypothetical here:
// one such row shipped as coverage for a full round before anybody noticed it was
// unreachable.
//
// Every behaviour below was reproduced against real ffmpeg before it was written down, and
// the numbers in the comments are measured rather than argued:
//
//   - refusesSegments: the playlist serves and every segment answers 403. ffprobe fails
//     naming "HTTP error 403 Forbidden" and "failed too many times", and the read produces 0
//     bytes.
//   - tricklesSegments: 24 KB/s against media published at 1.2 Mbit/s measured speed=0.383,
//     which is the field cast that died at 0.39.
//   - stallsMidSegment: half a fragment, then the socket is held open. With a mid-read
//     deadline this manufactures the field failure exactly ("Invalid NAL unit size", "Error
//     applying bitstream filters to an output packet for stream #0", "Error muxing a
//     packet", speed 0.132). With none, which is what castor's segment-fragile read row asks
//     for, it produces 0 bytes with an EMPTY stderr, a media position frozen at 2.02s and a
//     speed decaying 0.0515 then 0.0508. That last measurement is why the fragile policy is
//     safe to arm and why speed is the only signal there is to arm it against.
//   - acceptsAndSaysNothing: the segment request is accepted, headers are sent, and no body
//     ever follows.
//
// Two shapes were tried first and are recorded here because they do NOT reproduce anything:
// a 3-second fixture is too small for a trickle to bite (it measured 2.1x and completed),
// and a merely SHORT body is not a mid-read abort (ffmpeg exits 0 having quietly lost the
// tail). Hence a minute of media and a held socket rather than a truncated one.
type hostility int

const (
	// servesEverything is the control: the same fixture over an origin that behaves. Without
	// it, "no renderer was ever pointed at anything" passes just as well for a fixture castor
	// could never have cast in the first place.
	servesEverything hostility = iota
	// refusesSegments answers every segment 403. A refusal and not a 404, because 403 is
	// deliberately absent from the read policy's transient retry set (see read's transient),
	// so the reader gives up on the segment instead of waiting out a full backoff ceiling per
	// segment: this is the case that has to fail FAST for a user to be told anything useful.
	refusesSegments
	// tricklesSegments hands every segment over at trickleBytesPerSecond, which is the
	// starving upstream the whole deliverability judgement was written for.
	tricklesSegments
	// stallsMidSegment serves the first media segment whole and then writes half of the next
	// one and holds the socket. The first one is served on purpose: it is what lets the read
	// land media, state a speed and then be judged on it, which is the only signal a read
	// with no mid-read deadline leaves behind.
	stallsMidSegment
	// acceptsAndSaysNothing accepts the segment request, sends headers and never sends a
	// body: the tarpit a mid-read deadline exists for, and the shape that proves what
	// withholding one costs.
	acceptsAndSaysNothing
)

func (h hostility) String() string {
	switch h {
	case refusesSegments:
		return "refuses every segment"
	case tricklesSegments:
		return "trickles every segment"
	case stallsMidSegment:
		return "stalls partway through a segment"
	case acceptsAndSaysNothing:
		return "accepts a segment request and sends no body"
	default:
		return "serves everything"
	}
}

// hostileShape is what the origin publishes, and it is chosen for the read policy it
// selects rather than for the container: the three shapes here are the three rows of
// castor's read table that a network source can land on.
type hostileShape int

const (
	// tsSegments is an MPEG-TS segmented playlist: in-band framing, so the read table's
	// segment-in-band row answers it and KEEPS the configured mid-read deadline.
	tsSegments hostileShape = iota
	// fmp4Segments is an fMP4 segmented playlist: out-of-band framing, so the
	// segment-fragile row answers it and WITHHOLDS the mid-read deadline entirely, because a
	// fragment abandoned partway through desynchronises the bitstream filter a copy into
	// MPEG-TS cannot do without.
	fmp4Segments
	// wholeFile is one long GET, the row the configured rw_timeout was written for and the
	// only one that still applies it to a whole-file source.
	wholeFile
)

const (
	// hostileSeconds is how much media a hostile origin publishes.
	//
	// It is derived from the trickle, which is the slowest thing here: at
	// trickleBytesPerSecond against a fixture measured at roughly 114 KB per media second,
	// the origin hands over about one media second every five wall-clock seconds, so a minute
	// of media is five minutes of trickling and the origin is still mid-program when castor
	// rules on it. The first attempt at this used three seconds and proved nothing: the read
	// finished at 2.1x before any judgement could form.
	hostileSeconds = "60"

	// hostileVideoBitrate is what makes the trickle a starvation rather than an assumption. A
	// 320x240 fixture compresses to a handful of KB per second, which a 24 KB/s pipe delivers
	// faster than realtime; 1.2 Mbit/s at 640x360 measures 114 KB per media second, so the
	// same pipe delivers roughly a fifth of playback. The cases assert that ratio against the
	// fixture they were handed rather than trusting this number (see bytesPerMediaSecond).
	hostileVideoBitrate = "1200k"

	// trickleBytesPerSecond is the pipe segments are handed over on: a ~190 kbit/s link,
	// which against the media above is the ratio that produced the field's 0.39x.
	trickleBytesPerSecond = 24_000

	// trickleChunk is how much is written between sleeps. Small enough that the pacing is a
	// trickle rather than a burst per second, large enough that the case is not measuring
	// syscall overhead.
	trickleChunk = 4096
)

// hostileOrigin is a running hostile origin: what a cast is pointed at, and the media
// behind it.
type hostileOrigin struct {
	// URL is the playlist or file a cast is pointed at.
	URL string
	// ContentType is what resolution would have concluded the source is.
	ContentType string

	dir string
}

// startHostile generates a minute of media in the given shape and serves it from a real
// HTTP server that misbehaves in exactly one way.
func startHostile(t *testing.T, tl tools, shape hostileShape, how hostility) hostileOrigin {
	t.Helper()

	dir := t.TempDir()
	name, contentType := encodeHostileFixture(t, tl, dir, shape)

	// Closed when the case ends, and it is what releases a handler that is holding a socket
	// open. Two things need releasing and neither is optional: httptest.Server.Close waits
	// for its outstanding handlers, so a held one that watched only its request context would
	// wedge the case teardown for any read castor had not killed, and a handler parked on a
	// bare sleep would outlive the whole test binary.
	stop := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := os.ReadFile(filepath.Join(dir, filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// A playlist always serves, whole and at wire speed, and so does an fMP4 Media
		// Initialization Section. Every behaviour here is about the MEDIA: an origin that
		// withheld the playlist would be refused by the source probe before a read policy or a
		// health rule had a subject at all, which is a different failure and one the served-path
		// suites already cover.
		if strings.HasSuffix(r.URL.Path, ".m3u8") || filepath.Base(r.URL.Path) == hostileInitSegment {
			_, _ = w.Write(body)
			return
		}

		switch how {
		case refusesSegments:
			http.Error(w, "forbidden", http.StatusForbidden)
		case tricklesSegments:
			trickle(w, r, body)
		case stallsMidSegment:
			// One nominated fragment, half written and then held; every other segment serves
			// whole. Which fragment is the whole design of this behaviour, and both attempts that
			// failed are worth recording, because either one leaves a case quietly exercising a
			// different rule than the one it is named after.
			//
			// Stalling the SECOND fragment produces no measurement at all. ffmpeg is still inside
			// avformat_find_stream_info at that point (it wants several seconds of media, and it
			// had got as far as "Reinit context to 640x368" and a request for the third fragment),
			// so it never reaches its transcode loop, never writes a progress block and never
			// lands a byte: 0 bytes spooled and 0 speed samples over two and a half minutes. That
			// is the tarpit's shape and the tarpit case already covers it.
			//
			// Counting requests instead of naming one fails the same way for a different reason: a
			// cast makes TWO readers, and the source probe reads the head of the program before the
			// download starts, so a counter let the probe spend the whole segments the read needed.
			//
			// Ten seconds in, the read has stream info, a proven rate and media in the buffer, so
			// this is a stall of a cast in flight rather than one at the gate.
			if !strings.HasPrefix(filepath.Base(r.URL.Path), hostileStalledSegment) {
				_, _ = w.Write(body)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body[:len(body)/2])
			_ = http.NewResponseController(w).Flush()
			hold(r, stop)
		case acceptsAndSaysNothing:
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			_ = http.NewResponseController(w).Flush()
			hold(r, stop)
		default:
			_, _ = w.Write(body)
		}
	}))
	// Registered after the server's own cleanup so that it runs BEFORE it: cleanups run last
	// in first out, and closing the server first would block on the handler this releases.
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(stop) })

	t.Logf("hostile origin at %s: %s, publishing %ss of media as %s", server.URL, how, hostileSeconds, name)
	return hostileOrigin{URL: server.URL + "/" + name, ContentType: contentType, dir: dir}
}

// hold keeps the socket open with the response part written, which is the pathology.
// Closing it instead is a dropped connection, which the read policy's reconnect terms answer
// in a second, and truncating the body is a short read, which ffmpeg finishes at exit 0
// having lost the tail. Neither reaches anything castor has a rule about.
//
// It returns when the reader goes away (castor kills its ffmpeg on every fault it names
// itself, which severs the connection and cancels the request context) or when the case
// ends.
func hold(r *http.Request, stop <-chan struct{}) {
	select {
	case <-r.Context().Done():
	case <-stop:
	}
}

// trickle hands the body over at trickleBytesPerSecond.
//
// Each chunk is flushed, and that is load-bearing rather than tidy: net/http buffers a
// response, so an unflushed trickle arrives as one lump at the end and models a slow start
// rather than a slow pipe, which is precisely the difference between a read the
// deliverability rule convicts and one it never sees.
func trickle(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	const per = trickleChunk * time.Second / trickleBytesPerSecond
	for i := 0; i < len(body); i += trickleChunk {
		if _, err := w.Write(body[i:min(i+trickleChunk, len(body))]); err != nil {
			return
		}
		_ = http.NewResponseController(w).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(per):
		}
	}
}

const (
	// hostileInitSegment is the fMP4 Media Initialization Section's filename, named here
	// because the origin has to recognise it: a fragment stall is a stall of the MEDIA, and an
	// origin withholding the init segment is the tarpit case rather than this one.
	hostileInitSegment = "init.mp4"

	// hostileStalledSegment is the one fragment a stalling origin holds half written, ten media
	// seconds into the program. Ten because the read has to be past ffmpeg's stream-info
	// analysis (measured: several seconds of media) and past the playback gate's own confidence
	// window before it freezes, which is what makes the stall a stall of a cast in flight.
	hostileStalledSegment = "seg_010"
)

// encodeHostileFixture renders the media a hostile origin serves and returns what a cast is
// pointed at.
//
// The three shapes exist for the three read policies they select, not for the containers:
// what a case is about is whether the mid-read deadline applies, and that is decided by what
// the source published about its segments (see read.ShapeOf).
func encodeHostileFixture(t *testing.T, tl tools, dir string, shape hostileShape) (name, contentType string) {
	t.Helper()

	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=15:duration=" + hostileSeconds,
		"-f", "lavfi", "-i", "sine=frequency=440:duration=" + hostileSeconds,
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-profile:v", "baseline", "-pix_fmt", "yuv420p", "-g", "15", "-b:v", hostileVideoBitrate,
		"-c:a", "aac", "-ac", "2", "-shortest",
	}
	switch shape {
	case wholeFile:
		name, contentType = "movie.mp4", media.MP4
		// faststart so the moov atom is up front: a demuxer that had to seek to the end for it
		// would fail on the tarpit for want of a header rather than for want of media, which is
		// a different symptom entirely.
		args = append(args, "-movflags", "+faststart", filepath.Join(dir, name))
	case fmp4Segments:
		name, contentType = "index.m3u8", media.HLS
		args = append(args, hostilePlaylistArgs(dir, ".m4s")...)
		args = append(args, "-hls_segment_type", "fmp4",
			"-hls_fmp4_init_filename", hostileInitSegment, filepath.Join(dir, name))
	default:
		name, contentType = "index.m3u8", media.HLS
		args = append(args, hostilePlaylistArgs(dir, ".ts")...)
		args = append(args, filepath.Join(dir, name))
	}

	if out, err := exec.CommandContext(t.Context(), tl.ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("encoding the hostile origin's media: %v\n%s", err, out)
	}
	return name, contentType
}

// hostilePlaylistArgs publishes a COMPLETE playlist: every segment listed, EXT-X-ENDLIST
// written.
//
// A rolling live window would be the wrong fixture for every case here, and not by a little.
// A live source is read at exactly 1x with no burst, so the pace it was allowed is 1.0, and a
// read that was never allowed to run ahead cannot produce a deficit that means anything: the
// deliverability judgement withholds itself from it by design (see watch.Health.starving).
// Every trickle and stall row below would then be judged on silence alone, at a window two
// and a half times as long, and the rule they are about would never be reached.
func hostilePlaylistArgs(dir, segmentExt string) []string {
	return []string{
		"-f", "hls",
		"-hls_time", "1",
		"-hls_playlist_type", "vod",
		"-hls_list_size", "0",
		"-hls_segment_filename", filepath.Join(dir, "seg_%03d"+segmentExt),
	}
}

// bytesPerMediaSecond is the fixture's own rate, which is what makes the trickle case an
// assertion rather than a hope: the case checks it against the pipe the origin serves over,
// so a fixture that ever encodes smaller fails loudly instead of quietly measuring a
// perfectly deliverable read.
func (o hostileOrigin) bytesPerMediaSecond(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".m3u8") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	seconds, err := strconv.Atoi(hostileSeconds)
	if err != nil {
		t.Fatal(err)
	}
	return int(total) / seconds
}
