package ffmpeg

import (
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

// capturedBlocks is a verbatim -progress capture from a real ffmpeg run (a paced
// lavfi input remuxed to MPEG-TS with -stats_period 0.3). It is kept whole, padding
// and N/A and all, because every one of those shapes is a parse this code has to
// survive: ffmpeg answers N/A for every field until the first packet is muxed, and it
// right-aligns speed= into a fixed width.
const capturedBlocks = `frame=0
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
progress=end
`

// TestASampleCarriesPositionAndSpeed is the stage's measurement, read from real
// output. The parser used to keep out_time_us alone and throw the rest away, so the
// reader's only number was a byte count off the spool, which is the number that could
// not tell a stalled origin from a finished one.
func TestASampleCarriesPositionAndSpeed(t *testing.T) {
	samples := collectSamples(t, capturedBlocks)
	if len(samples) != 2 {
		t.Fatalf("got %d samples, want one per report block", len(samples))
	}

	// The first block is everything ffmpeg has to say before a packet is muxed, and
	// what it has to say is nothing. A zero sample is the honest reading of it.
	if want := (media.Progress{}); samples[0] != want {
		t.Errorf("the pre-first-packet block parsed as %+v, want %+v", samples[0], want)
	}

	want := media.Progress{
		Position: 1800 * time.Millisecond,
		Bytes:    33464,
		Speed:    79,
	}
	if samples[1] != want {
		t.Errorf("the muxing block parsed as %+v, want %+v", samples[1], want)
	}
}

// TestTheStarvedReadIsNameableFromItsOwnFeed is the failure this stage exists for,
// as the feed reports it: a read moving real bytes at real speed, and a speed field
// saying that what arrives in a second is less than a second of picture. Neither the
// byte count nor the bitrate says so, and the source published no rate to compare
// either against.
func TestTheStarvedReadIsNameableFromItsOwnFeed(t *testing.T) {
	samples := collectSamples(t, `bitrate=7203.1kbits/s
total_size=14390648
out_time_us=6396000
out_time=00:00:06.396000
speed=0.39x
progress=continue
`)
	if len(samples) != 1 {
		t.Fatalf("got %d samples, want 1", len(samples))
	}
	got := samples[0]
	if got.Speed != media.Speed(0.39) {
		t.Errorf("speed = %v, want 0.39: the number that named the failure", got.Speed)
	}
	if got.Bytes != 14390648 {
		t.Errorf("bytes = %d, want 14390648", got.Bytes)
	}
	if got.Position != 6396*time.Millisecond {
		t.Errorf("position = %s, want 6.396s", got.Position)
	}
}

// TestAFieldNoBlockCouldParseKeepsItsLastValue covers a stall as the feed reports it:
// ffmpeg keeps emitting blocks whose out_time and speed have gone back to N/A while
// nothing arrives. Resetting the sample there would report a reader as having gone
// backwards, and would invent a speed of zero (the one value a deliverability rule
// acts on) out of a line nobody parsed.
func TestAFieldNoBlockCouldParseKeepsItsLastValue(t *testing.T) {
	samples := collectSamples(t, `total_size=33464
out_time_us=1800000
speed=2.0x
progress=continue
total_size=N/A
out_time_us=N/A
speed=N/A
progress=continue
`)
	if len(samples) != 2 {
		t.Fatalf("got %d samples, want 2", len(samples))
	}
	if samples[1] != samples[0] {
		t.Errorf("an all-N/A block reported %+v, want the previous sample %+v", samples[1], samples[0])
	}
}

// TestSpeedIsReadThroughEveryShapeFFmpegWritesIt pins the one field with a format of
// its own. It is not a bare float: it carries an "x" suffix and right-aligned
// padding, and it is N/A before the first packet.
func TestSpeedIsReadThroughEveryShapeFFmpegWritesIt(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  media.Speed
		ok    bool
	}{
		{"0.109x", 0.109, true},
		{"  79x", 79, true},
		{"1.02x", 1.02, true},
		{"1.02e+03x", 1020, true},
		{"N/A", 0, false},
		{"", 0, false},
	} {
		got, ok := parseSpeed(strings.TrimSpace(tt.value))
		if ok != tt.ok {
			t.Errorf("parseSpeed(%q) ok = %t, want %t", tt.value, ok, tt.ok)
			continue
		}
		if ok && got != tt.want {
			t.Errorf("parseSpeed(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

// TestABlockWithoutItsTerminatorIsNotASample covers a feed cut off mid-block, which
// is what a killed process leaves behind. Emitting the half-read block would publish a
// sample whose fields describe two different instants.
func TestABlockWithoutItsTerminatorIsNotASample(t *testing.T) {
	samples := collectSamples(t, `total_size=33464
out_time_us=1800000
speed=2.0x
progress=continue
total_size=66928
out_time_us=3600000
`)
	if len(samples) != 1 {
		t.Fatalf("got %d samples, want 1: the truncated block has no progress= line", len(samples))
	}
}

// collectSamples runs the feed through WatchProgress and returns what it published.
func collectSamples(t *testing.T, feed string) []media.Progress {
	t.Helper()
	var samples []media.Progress
	WatchProgress(strings.NewReader(feed), func(s media.Progress) { samples = append(samples, s) })
	return samples
}
