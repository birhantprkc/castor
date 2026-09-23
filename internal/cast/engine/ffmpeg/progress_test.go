package ffmpeg

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

// A verbatim capture, then an all-N/A block (a stall), then a block cut before its terminator.
const capturedFeed = `frame=0
fps=0.00
stream_0_0_q=0.0
bitrate=N/A
total_size=0
out_time_us=N/A
out_time_ms=N/A
out_time=N/A
dup_frames=0
drop_frames=0
speed=N/A
progress=continue
frame=20
fps=9.98
stream_0_0_q=-1.0
bitrate= 148.7kbits/s
total_size=33464
out_time_us=1800000
out_time_ms=1800000
out_time=00:00:01.800000
dup_frames=0
drop_frames=0
speed=  79x
progress=continue
total_size=N/A
out_time_us=N/A
speed=N/A
progress=end
total_size=66928
out_time_us=3600000
`

func TestTheProgressFeedIsParsedPerBlock(t *testing.T) {
	var got []media.Progress
	WatchProgress(strings.NewReader(capturedFeed), func(s media.Progress) { got = append(got, s) })

	muxing := media.Progress{Position: 1800 * time.Millisecond, Bytes: 33464, Speed: 79}
	want := []media.Progress{{}, muxing, muxing}
	if !slices.Equal(got, want) {
		t.Errorf("samples = %+v, want %+v", got, want)
	}
}
