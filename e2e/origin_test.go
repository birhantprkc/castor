package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// liveSource describes a live HLS origin to stand up (zero value is TS-segmented AAC).
type liveSource struct {
	// SegmentType: "" (mpegts) or "fmp4"; frames AAC differently so castor must not assume.
	SegmentType string
	// SegmentExt disguises segments as ".jpg" to test embed-CDN handling.
	SegmentExt    string
	AudioCodec    string
	AudioChannels string
	VideoCodec    string
	// NoAudio publishes video alone, turning a pinned stream map into parse failure before read.
	NoAudio bool
	// Demuxed publishes tracks as separate renditions (HLS master with audio group); need re-mux.
	Demuxed bool
}

// liveSeconds outlasts reading; every second costs encode time.
const liveSeconds = "40"

type origin struct {
	PlaylistURL string
	AudioURL    string // Companion rendition of demuxed program, empty otherwise.
	dir         string
}

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
		"-t", liveSeconds, "-f", "lavfi", "-i", "testsrc=size=320x240:rate=15",
	}
	if !src.NoAudio {
		args = append(args, "-f", "lavfi", "-i", "sine=frequency=440")
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
		// Keep all segments (rolling window would lose segments the cell reads slower than wall clock).
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

	o := &origin{PlaylistURL: server.URL + "/" + playlist, dir: live}
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

	// Copy all files; reveal playlist progressively (unlisted segments are 404).
	entries, err := os.ReadDir(staged)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		// Don't copy sibling playlists (would expose staged EXT-X-ENDLIST).
		if filepath.Ext(e.Name()) == ".m3u8" || e.IsDir() {
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
	const joinable = 4 // Enough to join; then one per second.
	write(joinable)

	ctx, stop := context.WithCancel(t.Context())
	// Joined (not merely cancelled) so t.Errorf after test returns doesn't panic.
	done := make(chan struct{})
	t.Cleanup(func() { stop(); <-done })
	go func() {
		defer close(done)
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

// splitPlaylist separates header and segment entries for progressive reveal; entry is #EXTINF line + URI.
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
			// Exclude ENDLIST (reader would treat as VOD, not test live path).
		case len(segments) == 0:
			head.WriteString(lines[i])
		}
	}
	return head.String(), segments
}

// isLive reports what castor concludes, so test fails loudly if fixture stopped being live.
func (o *origin) isLive(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.PlaylistURL, nil)
	if err != nil {
		t.Fatalf("building origin request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("reading origin playlist: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("reading origin playlist body: %v", err)
	}
	text := string(body)
	return strings.Contains(text, "#EXTM3U") && !strings.Contains(text, "#EXT-X-ENDLIST")
}
