package e2e

import (
	"fmt"
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

// Real HTTP server with measured field behaviors: 403 rejection, trickling, mid-read stalls, tarpit holds.
type hostility int

const (
	// servesEverything is the control: verifies the test itself can detect a working cast.
	servesEverything hostility = iota
	// refusesSegments answers 403 (absent from transient retry set) so reader gives up.
	refusesSegments
	tricklesSegments
	// stallsMidSegment serves the first segment whole (lets read land media and speed) then holds.
	stallsMidSegment
	// acceptsAndSaysNothing is the tarpit a mid-read deadline exists for.
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

// hostileShape chooses the media format and policy row tested.
type hostileShape int

const (
	// tsSegments uses in-band framing, keeping the mid-read deadline.
	tsSegments hostileShape = iota
	// fmp4Segments uses out-of-band framing, witholding mid-read deadline to avoid bitstream filter desync.
	fmp4Segments
	// wholeFile uses the configured rw_timeout only.
	wholeFile
	// masterLadder publishes two TS-segmented rungs to test DeclaredEnvelope and height selection.
	masterLadder
)

const (
	// hostileSeconds = 60s makes trickles (at 24 KB/s against 114 KB/s media) finish mid-test.
	hostileSeconds = "60"

	// hostileVideoBitrate = 1.2 Mbit/s makes a 24 KB/s trickle a starvation (0.39x measured).
	hostileVideoBitrate = "1200k"

	// trickleBytesPerSecond reproduces field's 0.39x speed on a real pipe.
	trickleBytesPerSecond = 24_000

	// trickleChunk is slow-pipe pacing, not syscall overhead.
	trickleChunk = 4096

	// masterTallRung and masterShortRung: the tall one is chosen by declared BANDWIDTH.
	masterTallRung     = 360
	masterShortRung    = 180
	masterShortBitrate = "300k"
)

type hostileOrigin struct {
	URL string
	// ContentType is what resolution would have concluded the source is.
	ContentType string

	dir string
}

// startHostile generates a minute of media in the given shape and serves it from a real HTTP server.
func startHostile(t *testing.T, tl tools, shape hostileShape, how hostility) hostileOrigin {
	t.Helper()

	dir := t.TempDir()
	name, contentType := encodeHostileFixture(t, tl, dir, shape)

	// Cleanups run LIFO: closing the server first would block on the handler this releases.
	stop := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := os.ReadFile(filepath.Join(dir, filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// Playlist and fMP4 init always serve; behavior below is about the MEDIA segment.
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
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(stop) })

	t.Logf("hostile origin at %s: %s, publishing %ss of media as %s", server.URL, how, hostileSeconds, name)
	return hostileOrigin{URL: server.URL + "/" + name, ContentType: contentType, dir: dir}
}

func hold(r *http.Request, stop <-chan struct{}) {
	select {
	case <-r.Context().Done():
	case <-stop:
	}
}

// trickle hands the body over at trickleBytesPerSecond, flushing each chunk (unflushed arrives as one lump).
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
	// hostileInitSegment must be named so the origin can stall only the MEDIA, not the init.
	hostileInitSegment = "init.mp4"

	hostileStalledSegment = "seg_010"
)

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
		// faststart puts moov atom up front (demuxer seeking to end would fail on tarpit).
		args = append(args, "-movflags", "+faststart", filepath.Join(dir, name))
	case fmp4Segments:
		name, contentType = "index.m3u8", media.HLS
		args = append(args, hostilePlaylistArgs(dir, "seg_%03d.m4s")...)
		args = append(args, "-hls_segment_type", "fmp4",
			"-hls_fmp4_init_filename", hostileInitSegment, filepath.Join(dir, name))
	case masterLadder:
		name, contentType = "master.m3u8", media.HLS
		// One encode producing both rungs (scale filter) so the ladder costs a filter, not a second pass.
		args = append(args,
			"-filter_complex", fmt.Sprintf("[0:v]scale=-2:%d[short];[0:v]scale=-2:%d[tall]", masterShortRung, masterTallRung),
			"-map", "[short]", "-map", "1:a", "-map", "[tall]", "-map", "1:a",
			"-b:v:0", masterShortBitrate,
		)
		args = append(args, hostilePlaylistArgs(dir, "seg_%03d_%v.ts")...)
		args = append(args, "-var_stream_map", "v:0,a:0 v:1,a:1",
			"-master_pl_name", name, filepath.Join(dir, "rendition_%v.m3u8"))
	default:
		name, contentType = "index.m3u8", media.HLS
		args = append(args, hostilePlaylistArgs(dir, "seg_%03d.ts")...)
		args = append(args, filepath.Join(dir, name))
	}

	if out, err := exec.CommandContext(t.Context(), tl.ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("encoding the hostile origin's media: %v\n%s", err, out)
	}
	return name, contentType
}

// hostilePlaylistArgs publishes a complete VOD playlist so reads are read at exactly 1x (no burst).
func hostilePlaylistArgs(dir, segments string) []string {
	return []string{
		"-f", "hls",
		"-hls_time", "1",
		"-hls_playlist_type", "vod",
		"-hls_list_size", "0",
		"-hls_segment_filename", filepath.Join(dir, segments),
	}
}

// bytesPerMediaSecond sums every media file to verify the fixture encodes within the trickle pipe's capacity.
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
