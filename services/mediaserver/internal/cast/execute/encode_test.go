package execute

import (
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stupside/castor/services/mediaserver/internal/cast/deliver"
	"github.com/stupside/castor/services/mediaserver/internal/cast/recovery"
	"github.com/stupside/castor/services/mediaserver/internal/cast/transcode"
	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source"
)

// TestTheBurnInsCueReachesTheEncodeThatDrawsIt: the cue must be an input before the copy decision.
func TestTheBurnInsCueReachesTheEncodeThatDrawsIt(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	cuePath := filepath.Join(t.TempDir(), "cue.txt")
	if err := os.WriteFile(cuePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, buf := bufferedCast(t, ffmpegPath, ffprobePath)

	s.cast.Device = &fakeDevice{caps: dlnaLike()}
	opts, err := s.encode(feed{buffered: buf}, &fakeBurn{burnIn: cuePath})
	if err != nil {
		t.Fatalf("building the encode: %v", err)
	}
	cmd, err := transcode.EncodeArgs(opts)
	if err != nil {
		t.Fatalf("building the args: %v", err)
	}
	if !slices.ContainsFunc(cmd.Args, func(arg string) bool { return strings.Contains(arg, cuePath) }) {
		t.Errorf("no argument names the cue file the burn-in prepared: %v", cmd.Args)
	}
}

func TestTheBufferedEncodeIsMeasuredFromTheBufferAndNotTheSource(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	program, err := os.ReadFile(generateFixture(t, ffmpegPath, "buffer.ts", 1,
		[]string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "2", "-shortest", "-f", "mpegts"}))
	if err != nil {
		t.Fatal(err)
	}

	s, buf := bufferedCast(t, ffmpegPath, ffprobePath)
	if _, err := buf.reader.spool.Write(program); err != nil {
		t.Fatal(err)
	}
	// A source nothing can measure: nothing is listening on that port.
	s.attempt.Program = programFromStream(t, &source.Stream{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:1", Path: "/gone.mp4"}, ContentType: media.MP4})

	s.cast.Device = &fakeDevice{caps: dlnaLike()}
	opts, err := s.encode(feed{buffered: buf}, nil)
	if err != nil {
		t.Fatalf("building the encode: %v", err)
	}
	if opts.Video.Name() != "copy" || opts.Probe.VideoCodec != media.CodecH264 {
		t.Errorf("video %q from %+v, want a copy decided from the buffer's own h264", opts.Video.Name(), opts.Probe)
	}
}

// bufferedCast is the read-once composition's material for building an encode (empty buffer).
func bufferedCast(t *testing.T, ffmpegPath, ffprobePath string) (*pipeline, *buffered) {
	t.Helper()
	sp, err := deliver.NewSpool(filepath.Join(t.TempDir(), "spool.ts"))
	if err != nil {
		t.Fatal(err)
	}
	s := readingPipeline(t, realCast(&fakeDevice{}, ffmpegPath, ffprobePath), recovery.Attempt{})
	return s, &buffered{reader: &pull{spool: sp, done: make(chan struct{})}}
}
