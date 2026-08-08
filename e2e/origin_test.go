package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// liveSource describes a live HLS origin to stand up. The zero value is a
// TS-segmented AAC stream, which is what the overwhelming majority of sources in
// the wild look like.
type liveSource struct {
	// SegmentType is ffmpeg's -hls_segment_type: "" (mpegts) or "fmp4". It decides
	// how the source frames its AAC, which is the fact castor must not assume: the
	// same playlist extension serves ADTS from TS segments and out-of-band AAC from
	// fMP4 ones.
	SegmentType string
	// SegmentExt is the extension segments are written under. ".jpg" reproduces the
	// embed-CDN disguise, which an HTTP server then labels image/jpeg.
	SegmentExt string
	// AudioCodec is ffmpeg's -c:a for the origin.
	AudioCodec string
	// AudioChannels is -ac.
	AudioChannels string
	// VideoCodec is ffmpeg's -c:v.
	VideoCodec string
	// NoAudio publishes video alone, the shape that turns a pinned stream map into
	// an argument-parse failure before a byte is read.
	NoAudio bool
	// Demuxed publishes the two tracks as separate renditions, the shape an HLS
	// master with an audio group resolves to. Neither rendition is playable alone,
	// so reading such a program takes two inputs muxed back into one output.
	Demuxed bool
}

// liveSeconds is how much media each origin publishes. It only has to outlast
// what a cell consumes, and every second of it costs the suite encode time.
const liveSeconds = "40"

// origin is a running live HLS stream served over HTTP.
type origin struct {
	// PlaylistURL is what a cast is pointed at.
	PlaylistURL string
	// AudioURL is the companion rendition of a demuxed program, empty otherwise.
	AudioURL string
	dir      string
	playlist string
}

// startLive brings up a genuinely live HLS stream: a real ffmpeg encoding in real
// time (-re) into a rolling window with no EXT-X-ENDLIST, served by a real HTTP
// server. It never ends on its own, so it is killed when the test does, and it is
// what makes these tests different from the fixed-length fixtures the unit suites
// use: the playlist rolls while castor reads it, ffprobe finds no duration, and
// the cast has to be interrupted rather than waited out.
func startLive(t *testing.T, tl tools, src liveSource) *origin {
	t.Helper()

	if src.AudioCodec == "" {
		src.AudioCodec = "aac"
	}
	if src.AudioChannels == "" {
		src.AudioChannels = "2"
	}
	if src.VideoCodec == "" {
		src.VideoCodec = "libx264"
	}
	if src.SegmentExt == "" {
		src.SegmentExt = ".ts"
	}

	dir := t.TempDir()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		// Bounded, and written as fast as the machine allows rather than paced with
		// -re. Pacing the ORIGIN made it compete for CPU with the cell reading it: a
		// cell that also re-encodes audio then read under two seconds of media in
		// fourteen, and failed for want of packets its own fixture never published.
		//
		// Nothing is lost. What "live" has to mean here is that the stream announces
		// no end, which is omit_endlist below, and castor paces its own read at 1x
		// regardless. A CDN whose segments are already published is the ordinary case
		// anyway; a reader chasing an encoder is the exception.
		"-t", liveSeconds, "-f", "lavfi", "-i", "testsrc=size=320x240:rate=15",
	}
	if !src.NoAudio {
		args = append(args, "-re", "-f", "lavfi", "-i", "sine=frequency=440")
	}
	args = append(args,
		"-c:v", src.VideoCodec, "-pix_fmt", "yuv420p", "-g", "15",
	)
	if src.VideoCodec == "libx264" {
		args = append(args, "-preset", "ultrafast", "-tune", "zerolatency", "-profile:v", "baseline")
	}
	if src.VideoCodec == "libx265" {
		args = append(args, "-preset", "ultrafast", "-tag:v", "hvc1")
	}
	if src.VideoCodec == "libvpx-vp9" {
		args = append(args, "-b:v", "300k", "-deadline", "realtime", "-cpu-used", "8")
	}
	if !src.NoAudio {
		args = append(args, "-c:a", src.AudioCodec, "-ac", src.AudioChannels, "-shortest")
	}
	args = append(args,
		"-f", "hls",
		"-hls_time", "1",
		// A window wide enough that the fixture is never the bottleneck. Four seconds
		// starves a cell that re-encodes audio and reads at wall-clock speed: it falls
		// behind, segments roll off underneath it, and the run dies of a hostile origin
		// rather than of anything castor did.
		// The playlist keeps every segment it has published. A rolling window would
		// be closer to a real CDN, but it makes the ORIGIN the thing under test: a
		// cell that re-encodes audio reads slower than wall clock, falls behind a
		// window that deletes, and fails for want of a segment castor did nothing
		// wrong to miss. What this suite needs from "live" is that the stream has no
		// end, and omit_endlist alone gives that: no EXT-X-ENDLIST, no duration, a
		// playlist that grows while it is being read.
		"-hls_list_size", "0",
		"-hls_flags", "independent_segments",
	)
	if src.SegmentType != "" {
		args = append(args, "-hls_segment_type", src.SegmentType,
			"-hls_fmp4_init_filename", "init.mp4")
	}

	playlist := "stream.m3u8"
	if src.Demuxed {
		playlist = "rendition_0.m3u8"
		args = append(args,
			"-map", "0:v", "-map", "1:a",
			"-var_stream_map", "v:0,agroup:aud a:0,agroup:aud",
			"-hls_segment_filename", filepath.Join(dir, "seg_%v_%05d"+src.SegmentExt),
			filepath.Join(dir, "rendition_%v.m3u8"),
		)
	} else {
		args = append(args,
			"-hls_segment_filename", filepath.Join(dir, "seg_%05d"+src.SegmentExt),
			filepath.Join(dir, playlist),
		)
	}

	if out, err := exec.Command(tl.ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("encoding the origin's media: %v\n%s", err, out)
	}

	live := t.TempDir()
	server := httptest.NewServer(http.FileServer(http.Dir(live)))
	t.Cleanup(server.Close)

	o := &origin{PlaylistURL: server.URL + "/" + playlist, dir: live, playlist: playlist}
	o.publish(t, dir, playlist)
	if src.Demuxed {
		o.AudioURL = server.URL + "/rendition_1.m3u8"
		o.publish(t, dir, "rendition_1.m3u8")
	}
	return o
}

func (o *origin) publish(t *testing.T, staged, playlist string) {
	t.Helper()

	full, err := os.ReadFile(filepath.Join(staged, playlist))
	if err != nil {
		t.Fatalf("reading the staged playlist: %v", err)
	}
	header, segments := splitPlaylist(string(full))

	// Copy every file the playlist can ever reference. Only the playlist itself is
	// revealed progressively; a segment that appears before it is listed is invisible
	// to a reader, and one that appears after it is listed is a 404.
	entries, err := os.ReadDir(staged)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == playlist || e.IsDir() {
			continue
		}
		media, err := os.ReadFile(filepath.Join(staged, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(o.dir, e.Name()), media, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write := func(n int) {
		body := header + strings.Join(segments[:min(n, len(segments))], "")
		if err := os.WriteFile(filepath.Join(o.dir, playlist), []byte(body), 0o600); err != nil {
			t.Errorf("publishing the playlist: %v", err)
		}
	}
	// Enough to join, then one per second for as long as the cell reads.
	const joinable = 4
	write(joinable)

	ctx, stop := context.WithCancel(t.Context())
	t.Cleanup(stop)
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for n := joinable; n < len(segments); n++ {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			write(n + 1)
		}
	}()
}

// splitPlaylist separates a playlist's header from its segment entries, so the
// entries can be revealed a few at a time. An entry is its #EXTINF line plus the
// URI that follows it, which is the only grouping HLS guarantees.
func splitPlaylist(playlist string) (header string, segments []string) {
	lines := strings.SplitAfter(playlist, "\n")
	var head strings.Builder
	for i := 0; i < len(lines); i++ {
		switch {
		case strings.HasPrefix(lines[i], "#EXTINF"):
			if i+1 < len(lines) {
				segments = append(segments, lines[i]+lines[i+1])
				i++
			}
		case strings.HasPrefix(lines[i], "#EXT-X-ENDLIST"):
			// The staged playlist is complete; the published one must not be, or a
			// reader treats it as VOD and the cast never exercises the live path.
		case len(segments) == 0:
			head.WriteString(lines[i])
		}
	}
	return head.String(), segments
}

// isLive reports what castor will conclude about the source, so a test that means
// to exercise the live path fails loudly if its fixture stopped being live.
func (o *origin) isLive(t *testing.T, tl tools) bool {
	t.Helper()
	out, err := exec.Command(tl.ffprobe,
		"-v", "error",
		"-allowed_extensions", "ALL",
		"-allowed_segment_extensions", "ALL",
		"-extension_picky", "0",
		"-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1",
		o.PlaylistURL,
	).Output()
	if err != nil {
		t.Fatalf("probing origin: %v", err)
	}
	d := strings.TrimSpace(string(out))
	return d == "" || d == "N/A"
}

func (o *origin) String() string { return fmt.Sprintf("live origin at %s", o.PlaylistURL) }
