package ffmpeg

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/media"
)

// The containers these tests encode into, resolved from the registry exactly as
// the pipeline resolves them, so a test never describes a format the production
// path could not have produced.
var (
	mpegtsFormat = testFormat(media.MPEGTS)
	mp4Format    = testFormat(media.MP4)
	hlsFormat    = testFormat(media.HLS)
)

func testFormat(contentType string) media.FormatInfo {
	f, ok := media.FormatForContentType(contentType)
	if !ok {
		panic("no producible format for " + contentType)
	}
	return f
}

// testReadDeadline is the configured mid-read timeout these tests read with: the
// same thirty seconds the shipped configuration carries.
const testReadDeadline = 30 * time.Second

// The read policies these tests render, taken from the read table exactly as the
// pipeline takes them, so no test ever renders terms production could not have
// chosen.
var (
	vodRead     = testRead(read.Shape{Segmented: true})
	liveRead    = testRead(read.Shape{Segmented: true, Live: true})
	fragileRead = testRead(read.Shape{Segmented: true, Framing: media.FramingOutOfBand})
	longGETRead = testRead(read.Shape{})
)

func testRead(shape read.Shape) read.Policy {
	p, err := read.For(shape, testReadDeadline)
	if err != nil {
		panic(err)
	}
	return p
}

// argValue returns the token immediately after the last occurrence of flag, or
// "" if the flag is absent. ffmpeg options are flag/value pairs, so this reads
// the value the encoder will actually receive.
func argValue(args []string, flag string) string {
	for i := len(args) - 2; i >= 0; i-- {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func hasFlag(args []string, flag string) bool {
	return slices.Contains(args, flag)
}

// aacAudio is the audio decision used by every test that is not about audio: a
// plain AAC re-encode. Both axes have to be decided for a command line to be
// buildable at all, so the tests that only care about video still have to say
// what the audio is doing, and saying it once here keeps them readable.
var aacAudio = EncodeAudio(AudioEncode{Codec: media.CodecAAC})

func TestEncodeArgsCopyRemux(t *testing.T) {
	// CopyVideo stream-copies the video.
	args, err := EncodeArgs(EncodeOptions{
		PipeFormat: SpoolFormat,
		Format:     mpegtsFormat,
		Video:      CopyVideo(),
		Audio:      EncodeAudio(AudioEncode{Codec: media.CodecAAC, Bitrate: "256k", SampleRate: 48000, Channels: 2}),
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := argValue(args, "-c:v"); got != "copy" {
		t.Fatalf("video codec = %q, want copy", got)
	}
	if hasFlag(args, "-preset") {
		t.Error("copy must not set -preset")
	}
	if hasFlag(args, "-b:v") {
		t.Error("copy must not set -b:v")
	}
	if hasFlag(args, "-vf") {
		t.Error("copy must not scale (no -vf)")
	}
	if hasFlag(args, "-init_hw_device") {
		t.Error("copy must not initialise a hardware device")
	}
	// A copied bitstream can't be re-rate-controlled, re-formatted, or
	// re-keyframed: those flags target the encoder, which isn't running.
	for _, f := range []string{"-maxrate", "-bufsize", "-pix_fmt", "-force_key_frames"} {
		if hasFlag(args, f) {
			t.Errorf("copy must not set %s", f)
		}
	}
	if got := argValue(args, "-c:a"); got != "aac" {
		t.Errorf("audio codec = %q, want aac", got)
	}
}

// TestEncodeArgsAudioRepack is the framing rule, both directions. AAC arriving
// from an in-band container carries an ADTS header per frame; a container that
// declares its decoder configuration once up front needs those stripped, and one
// that expects them is destroyed by the same filter.
//
// The wrong direction is the one that matters, because nothing reports it:
// aac_adtstoasc handed to the mpegts muxer exits 0, prints "AAC bitstream not in
// ADTS format and extradata missing" once per packet, leaves 8 of 189, and
// produces audio nothing can decode.
func TestEncodeArgsAudioRepack(t *testing.T) {
	for _, tt := range []struct {
		name   string
		format media.FormatInfo
		audio  AudioTrack
		want   string
	}{
		{"a copy into a fragmented mp4 is stripped", mp4Format, CopyAudio(), "aac_adtstoasc"},
		{"a copy into HLS is stripped", hlsFormat, CopyAudio(), "aac_adtstoasc"},
		{"a copy into MPEG-TS keeps its headers", mpegtsFormat, CopyAudio(), ""},
		// A re-encode produces the muxer's own framing, so it needs no filter, and a
		// stray one aborts at filter init before anything is written.
		{"a re-encode carries no filter", mp4Format, aacAudio, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := mustEncodeArgs(t, EncodeOptions{
				PipeFormat: SpoolFormat,
				Format:     tt.format,
				Probe:      media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC},
				Video:      CopyVideo(),
				Audio:      tt.audio,
			})
			if got := argValue(args, "-bsf:a"); got != tt.want {
				t.Errorf("-bsf:a = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReencodeRefusesAMisScopedRepack proves the rule behind the test below is
// structural rather than a coincidence of today's table. A repack rewrites framing
// a track already has, so a row contributing one must be gated on the copying
// predicate; a row that forgets is refused while the command line is being built,
// instead of reaching ffmpeg and aborting at filter init with a message that names
// neither castor nor the row that caused it.
func TestReencodeRefusesAMisScopedRepack(t *testing.T) {
	audioCopyAdaptations = append(audioCopyAdaptations, copyAdaptation{
		Name:    "test-repack-missing-its-copying-predicate",
		When:    func(copySubject) bool { return true },
		Filters: []string{"aac_adtstoasc"},
	})
	defer func() { audioCopyAdaptations = audioCopyAdaptations[:len(audioCopyAdaptations)-1] }()

	_, err := EncodeArgs(EncodeOptions{
		PipeFormat: SpoolFormat,
		Format:     mp4Format,
		Probe:      media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC},
		Video:      CopyVideo(),
		Audio:      aacAudio,
	})
	if err == nil {
		t.Fatal("want an error for a repack row that is not gated on copying, got nil")
	}
}

// TestMovFlagsMergeBaseAndAdaptation pins that an adaptation's tokens are merged
// into the muxer's base rather than emitted as a second -movflags, which would
// clobber it: -movflags is an AVOption, so the last one on the command line wins,
// and losing +empty_moov breaks every mp4 cast on a pipe.
func TestMovFlagsMergeBaseAndAdaptation(t *testing.T) {
	base := "+frag_keyframe+empty_moov+default_base_moof"

	ac3 := mustEncodeArgs(t, EncodeOptions{
		PipeFormat: SpoolFormat,
		Format:     mp4Format,
		Probe:      media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: media.CodecAC3},
		Video:      CopyVideo(),
		Audio:      CopyAudio(),
	})
	if got, want := argValue(ac3, "-movflags"), base+"+delay_moov"; got != want {
		t.Errorf("-movflags = %q, want %q (AC-3's dac3 box is derived from the first frame)", got, want)
	}
	if countFlag(ac3, "-movflags") != 1 {
		t.Error("-movflags must be emitted exactly once: it is an AVOption and the last one wins")
	}
	// The -frag_duration that bounds delay_moov's silence rides alongside it.
	if got := argValue(ac3, "-frag_duration"); got != "1000000" {
		t.Errorf("-frag_duration = %q, want 1000000 so delay_moov's dead socket is bounded", got)
	}

	aac := mustEncodeArgs(t, EncodeOptions{
		PipeFormat: SpoolFormat,
		Format:     mp4Format,
		Probe:      media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC},
		Video:      CopyVideo(),
		Audio:      CopyAudio(),
	})
	if got := argValue(aac, "-movflags"); got != base {
		t.Errorf("-movflags = %q, want the bare base %q: delay_moov withholds every byte until the first packet of every mapped track", got, base)
	}
	if hasFlag(aac, "-frag_duration") {
		t.Error("-frag_duration must not appear without the delay it exists to bound")
	}
}

// TestStrictIsAnOutputOption pins the placement, which matters:
// given before -i, -strict -2 never reaches the muxer and a copied TrueHD track
// still dies at exit 88 ("truehd in MP4 support is experimental").
func TestStrictIsAnOutputOption(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts EncodeOptions
	}{
		{"pipe input", EncodeOptions{PipeFormat: SpoolFormat, Format: mp4Format, Video: CopyVideo(), Audio: aacAudio}},
		{"network input", EncodeOptions{Source: NetworkSource{URL: mustURL(t, "http://example.test/in.mkv"), ContentType: media.MKV}, Format: mp4Format, Video: CopyVideo(), Audio: aacAudio}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := mustEncodeArgs(t, tt.opts)
			strict := slices.Index(args, "-strict")
			if strict < 0 {
				t.Fatal("-strict -2 is missing; it is the only thing that unlocks a copied TrueHD track into the mp4 family")
			}
			lastInput := slices.Index(args, "-i")
			for i, a := range args {
				if a == "-i" {
					lastInput = i
				}
			}
			if strict < lastInput {
				t.Errorf("-strict is at %d, before the last -i at %d; there it never reaches the muxer", strict, lastInput)
			}
		})
	}
}

// TestMapsAreOptional pins the "?" suffix. A pinned map turns a missing track
// into an argument-parse failure before a single byte is read: it fails
// reporting that the map matched no streams, for audio-only, video-only and
// video-only HLS sources alike, on all three destinations. Castor must never
// reject a source for a shape ffmpeg will happily produce output from.
func TestMapsAreOptional(t *testing.T) {
	muxed := NetworkSource{URL: mustURL(t, "http://example.test/in.mkv"), ContentType: media.MKV}
	demuxed := NetworkSource{
		URL:         mustURL(t, "http://example.test/video.m3u8"),
		AudioURL:    mustURL(t, "http://example.test/audio.m3u8"),
		ContentType: media.HLS,
	}

	for _, tt := range []struct {
		name string
		args []string
	}{
		{"encode", mustEncodeArgs(t, EncodeOptions{Source: muxed, Format: mp4Format, Video: CopyVideo(), Audio: aacAudio})},
		{"pull", PullArgs(PullOptions{Source: muxed})},
	} {
		if !hasFlag(tt.args, "0:v:0?") {
			t.Errorf("%s: video map is not optional; a video-less source fails at argument parse", tt.name)
		}
		if got := argValue(tt.args, "-map"); got != "0:a:0?" {
			t.Errorf("%s: audio map = %q, want 0:a:0?", tt.name, got)
		}
	}
	if got := argValue(PullArgs(PullOptions{Source: demuxed}), "-map"); got != "1:a:0?" {
		t.Errorf("demuxed pull audio map = %q, want 1:a:0? (the audio is the second input)", got)
	}
}

// TestPullArgsReencodesRequestedAxes pins the pull's runtime self-correction at
// the arg level: the zero ReencodeAxes copies both halves, and an axis the spool
// container cannot carry is encoded into the floor MPEG-TS does
// carry, leaving the other half a copy.
func TestPullArgsReencodesRequestedAxes(t *testing.T) {
	src := NetworkSource{URL: mustURL(t, "http://example.test/in.webm"), ContentType: media.WebM}

	plain := PullArgs(PullOptions{Source: src})
	if got := argValue(plain, "-c:v"); got != codecCopy {
		t.Errorf("video codec = %q, want copy for an ordinary pull", got)
	}
	if got := argValue(plain, "-c:a"); got != codecCopy {
		t.Errorf("audio codec = %q, want copy for an ordinary pull", got)
	}

	videoOnly := PullArgs(PullOptions{Source: src, Reencode: carriage.Axes{Video: true}})
	if got := argValue(videoOnly, "-c:v"); got != "libx264" {
		t.Errorf("video codec = %q, want libx264 when the spool container cannot carry the bitstream", got)
	}
	if got := argValue(videoOnly, "-c:a"); got != codecCopy {
		t.Errorf("audio codec = %q, want copy: only the affected axis degrades", got)
	}

	both := PullArgs(PullOptions{Source: src, Reencode: carriage.Axes{Video: true, Audio: true}})
	if got := argValue(both, "-c:a"); got != "aac" {
		t.Errorf("audio codec = %q, want aac", got)
	}
}

// TestThePullsFloorEncodeIsRateCapped covers the one rate-control authority in castor that
// used to have no ceiling at all.
//
// A bare -crf is unbounded by construction, and this is the one encode that can be asked
// for on a source nobody chose the resolution of: at 3840x2160 it asks a veryfast software
// encoder for tens of Mbit/s in realtime, which it cannot hold, and a read that cannot hold
// realtime is one the deliverability judgement convicts (speed=0.0627). So the recovery for
// a copy that broke upstream would manufacture the undeliverable cast it exists to escape.
//
// The cap is asserted to BE the decision layer's own H.264 budget rather than to be some
// number, because two producers of the floor codec aiming at different ceilings would spend
// the quality twice.
func TestThePullsFloorEncodeIsRateCapped(t *testing.T) {
	src := NetworkSource{URL: mustURL(t, "http://example.test/in.m3u8"), ContentType: media.HLS}
	args := PullArgs(PullOptions{Source: src, Reencode: carriage.Axes{Video: true}})

	if got := argValue(args, "-crf"); got != "23" {
		t.Errorf("-crf = %q, want the quality target kept: the cap constrains it, it does not replace it", got)
	}
	if got := argValue(args, "-maxrate"); got != media.FloorVideoMaxrate {
		t.Errorf("-maxrate = %q, want the floor's own VBV cap %q", got, media.FloorVideoMaxrate)
	}
	if got := argValue(args, "-bufsize"); got != media.FloorVideoBufsize {
		t.Errorf("-bufsize = %q, want the window the cap is measured over (%q)", got, media.FloorVideoBufsize)
	}

	// And nothing of the sort on an ordinary pull: a stream copy carries no rate control,
	// since the source's bitrate is whatever it is.
	plain := PullArgs(PullOptions{Source: src})
	if slices.Contains(plain, "-maxrate") || slices.Contains(plain, "-crf") {
		t.Errorf("a copying pull carries rate control (%v), which a copied bitstream has nowhere to apply", plain)
	}
}

// TestThePullsFloorEncodeIsResolutionCapped is the other half of that rate control, and the
// half the VBV cap cannot do: the cap bounds the BITS, while what a veryfast software
// encoder cannot hold at 3840x2160 is the pixel rate.
//
// Uncapped, this read measures under realtime on ordinary hardware, the gate reports the
// SOURCE as delivering less than playback consumes, and the recovery that asked for this
// encode (an axis a previous copy died on) abandons the links it was reaching for. The
// escape manufactures the verdict it exists to escape, and on a 4K source that is the only
// outcome it has.
//
// The filter is asserted to be the SAME expression the served encode caps with, because the
// buffer this read writes is read by that encode: two producers scaling differently would
// mean the pixels this one spent are thrown away by the next.
func TestThePullsFloorEncodeIsResolutionCapped(t *testing.T) {
	src := NetworkSource{URL: mustURL(t, "http://example.test/in.m3u8"), ContentType: media.HLS}
	const ceiling = 1080

	args := PullArgs(PullOptions{Source: src, Reencode: carriage.Axes{Video: true}, MaxHeight: ceiling})
	served := mustEncodeArgs(t, EncodeOptions{
		Source: src, Format: mp4Format, Audio: aacAudio,
		Video: EncodeVideo(VideoEncode{Encoder: softwareVideoEncoder(t), MaxHeight: ceiling}),
	})
	if got, want := argValue(args, "-vf"), argValue(served, "-vf"); got != want {
		t.Errorf("the read's floor encode scales with %q while the encode reading its buffer scales with %q; the ceiling has to be one expression or the read spends pixels the next process discards", got, want)
	}
	if got := argValue(args, "-vf"); !strings.Contains(got, "1080") {
		t.Errorf("-vf = %q, want the cast's own height ceiling: unscaled this encode runs at the source's resolution, which is what makes the read slower than playback", got)
	}

	// A cast with no ceiling is the zero-value convention (see core.Resolve), and a copying
	// pull has nowhere to apply a filter at all: a copied bitstream cannot be scaled.
	if got := PullArgs(PullOptions{Source: src, Reencode: carriage.Axes{Video: true}}); hasFlag(got, "-vf") {
		t.Errorf("a pull with no height ceiling carries a scale filter: %q", got)
	}
	if got := PullArgs(PullOptions{Source: src, MaxHeight: ceiling}); hasFlag(got, "-vf") {
		t.Errorf("a copying pull carries a scale filter, which ffmpeg answers with \"Filtering and streamcopy cannot be used together\": %q", got)
	}
}

// TestTheReadStatesItsOwnReportPeriod pins the second half of a derivation whose two halves
// live in different packages. The playback gate holds for a derived number of this reader's
// report blocks before a stated speed may convict a link (watch.minSpeedSamples, checked
// against read.StatsPeriod there), and that arithmetic is only true of the reader if the
// reader really reports on that period.
//
// Left to ffmpeg's default it was true by coincidence, and an ffmpeg that reported once a
// second would have halved the evidence behind every deliverability verdict with nothing
// anywhere to notice.
func TestTheReadStatesItsOwnReportPeriod(t *testing.T) {
	src := NetworkSource{URL: mustURL(t, "http://example.test/in.m3u8"), ContentType: media.HLS, Read: vodRead}
	args := PullArgs(PullOptions{Source: src})

	got := argValue(args, "-stats_period")
	if want := formatSeconds(read.StatsPeriod); got != want {
		t.Errorf("-stats_period = %q, want %q: the confidence window the gate holds for is counted in these blocks", got, want)
	}
	if got != "0.5" {
		t.Errorf("-stats_period = %q; the period is written out here because the number the gate's hold is derived from has to be the number the reader was told", got)
	}
}

// TestLogLevelNeverHidesTheSilentTell pins the one thing that would blind the
// silent-failure detector. The MPEG-TS muxer's private-data loss and
// discardcorrupt's packet drops are both WARNING lines with the exit code left at
// 0, so -loglevel error makes them invisible.
func TestLogLevelNeverHidesTheSilentTell(t *testing.T) {
	src := NetworkSource{URL: mustURL(t, "http://example.test/in.mkv"), ContentType: media.MKV}
	if got := argValue(PullArgs(PullOptions{Source: src}), "-loglevel"); got == "error" || got == "quiet" || got == "fatal" || got == "panic" {
		t.Errorf("pull -loglevel = %q; the muxer's private-data warning is warning-level and would be hidden", got)
	}
	// The encode emits no -loglevel at all and so runs at ffmpeg's default (info),
	// which sees the same lines.
	if hasFlag(mustEncodeArgs(t, EncodeOptions{Source: src, Format: mp4Format, Video: CopyVideo(), Audio: aacAudio}), "-loglevel") {
		t.Error("the encode must not set -loglevel: ffmpeg's default already shows the silent-failure warnings")
	}
}

// TestTheReaderReportsOnItselfAndTeesPCMOnSeparatePipes pins the fd routing of the
// pull's two extra outputs. The numbers are written out rather than taken from the
// constants under test, because the whole risk here is a renumbering: -progress and
// the PCM tee both live on extra pipes, they are opened positionally (ExtraFiles[0] is
// fd 3), and transposing them is silent in both directions.
func TestTheReaderReportsOnItselfAndTeesPCMOnSeparatePipes(t *testing.T) {
	src := NetworkSource{URL: mustURL(t, "http://example.test/in.m3u8"), ContentType: media.HLS, Read: vodRead}

	plain := PullOptions{Source: src}
	args := PullArgs(plain)
	if !containsSequence(args, []string{"-progress", "pipe:3"}) {
		t.Errorf("the pull reports on itself nowhere: %q", args)
	}
	if slices.Contains(args, "pipe:4") {
		t.Error("a pull with no PCM tee must not route anything to the second extra pipe")
	}
	if got := plain.ExtraPipes(); got != 1 {
		t.Errorf("extra pipes = %d, want 1 for the progress feed alone", got)
	}

	withPCM := PullOptions{Source: src, PCM: true, PCMSampleRate: 16000}
	args = PullArgs(withPCM)
	if !containsSequence(args, []string{"-progress", "pipe:3"}) {
		t.Errorf("the PCM tee displaced the progress feed: %q", args)
	}
	// The PCM output is the LAST thing on the command line, so its target is the last
	// token: an s16le output on pipe:4, whose fd only exists because pipe:3 does.
	if !containsSequence(args, []string{"-f", "s16le", "pipe:4"}) {
		t.Errorf("the PCM tee is not on the second extra pipe: %q", args)
	}
	if got := withPCM.ExtraPipes(); got != 2 {
		t.Errorf("extra pipes = %d, want 2: the progress feed and the PCM tee", got)
	}
}

// TestEveryEncodeReportsOnItself is the other half of the reader's telemetry: the
// encode leg used to emit -progress only when it was burning subtitles, so the two
// legs of a cast that starved could each be described only by the bytes they had
// written. The report period is the one part that is still conditional, and for a
// reason that is about cues and not about telemetry.
func TestEveryEncodeReportsOnItself(t *testing.T) {
	src := NetworkSource{URL: mustURL(t, "http://example.test/in.mkv"), ContentType: media.MKV, Read: longGETRead}
	base := EncodeOptions{Source: src, Format: mp4Format, Audio: aacAudio}

	for _, tt := range []struct {
		name        string
		video       VideoTrack
		wantPeriod  string
		wantPeriodP bool
	}{{
		name:  "a stream copy",
		video: CopyVideo(),
	}, {
		name:  "a re-encode with nothing burned in",
		video: EncodeVideo(VideoEncode{Encoder: softwareVideoEncoder(t), MaxHeight: 1080}),
	}, {
		name:        "a burn-in, whose cue placement granularity is the report period",
		video:       EncodeVideo(VideoEncode{Encoder: softwareVideoEncoder(t), SubtitleTextFile: "/tmp/cue.txt"}),
		wantPeriod:  "0.1",
		wantPeriodP: true,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			opts := base
			opts.Video = tt.video
			args := mustEncodeArgs(t, opts)
			if !containsSequence(args, []string{"-progress", "pipe:3"}) {
				t.Errorf("the encode reports on itself nowhere: %q", args)
			}
			if got := hasFlag(args, "-stats_period"); got != tt.wantPeriodP {
				t.Errorf("-stats_period present = %t, want %t", got, tt.wantPeriodP)
			}
			if tt.wantPeriodP {
				if got := argValue(args, "-stats_period"); got != tt.wantPeriod {
					t.Errorf("-stats_period = %q, want %q so a cue lands on the frame it belongs to", got, tt.wantPeriod)
				}
			}
		})
	}

	if EncodeExtraPipes != 1 {
		t.Errorf("EncodeExtraPipes = %d, want 1: an encode routes only its progress feed to an extra pipe", EncodeExtraPipes)
	}
}

// softwareVideoEncoder is the floor encoder from the registry, so a test that needs a
// re-encode names one production could have selected.
func softwareVideoEncoder(t *testing.T) Encoder {
	t.Helper()
	enc, ok := softwareBaseline(media.FloorVideoCodec)
	if !ok {
		t.Fatalf("the encoder registry has no software baseline for %q", media.FloorVideoCodec)
	}
	return enc
}

// TestHLSOutputComesFromContainerTuning pins that the segmented output is data on
// the muxer's tuning row rather than a delivery-kind branch, so a second
// segmented format would declare its own manifest name instead of inheriting HLS's.
func TestHLSOutputComesFromContainerTuning(t *testing.T) {
	args := mustEncodeArgs(t, EncodeOptions{
		Source: NetworkSource{URL: mustURL(t, "http://example.test/in.mkv"), ContentType: media.MKV},
		Format: hlsFormat,
		Video:  CopyVideo(),
		Audio:  aacAudio,
	})
	if got := argValue(args, "-f"); got != hlsFormat.Muxer {
		t.Errorf("-f = %q, want the format's own muxer %q", got, hlsFormat.Muxer)
	}
	if got := argValue(args, "-hls_segment_type"); got != "fmp4" {
		t.Errorf("-hls_segment_type = %q, want fmp4 (the HLS row declares FramingOutOfBand for this reason)", got)
	}
	if got := args[len(args)-1]; got != media.HLSPlaylistName {
		t.Errorf("last arg = %q, want the playlist %q", got, media.HLSPlaylistName)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestEncodeArgsReadrateHeadroom(t *testing.T) {
	// Burning subtitles paces the encode with -readrate. It must be just above
	// realtime (read.EncodePace), not 1.0: at dead-even playback speed the
	// renderer's buffer has no headroom to rebuild after jitter and rebuffers.
	args, err := EncodeArgs(EncodeOptions{
		PipeFormat: SpoolFormat,
		Format:     mpegtsFormat,
		Video:      EncodeVideo(VideoEncode{Encoder: libx264, SubtitleTextFile: "/tmp/cue.txt"}),
		Audio:      aacAudio,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := formatRate(read.EncodePace.Realtime); argValue(args, "-readrate") != want {
		t.Errorf("readrate = %q, want %q (headroom above realtime)", argValue(args, "-readrate"), want)
	}
	if read.EncodePace.Realtime <= 1.0 {
		t.Errorf("the encode is paced at %v, which leaves the renderer's buffer no headroom to rebuild after jitter", read.EncodePace.Realtime)
	}
}

// TestEncodeArgsPerEncoder pins what each encoder is handed. Rate control and the
// GOP expression are identical everywhere, so the runner asserts those once and a
// row states only what makes its encoder different, plus the flags it must not
// pick up. The forbidden half carries the weight here: every bug this has had was
// a flag leaking from one encoder to another, and ffmpeg takes most of them
// without complaint.
func TestEncodeArgsPerEncoder(t *testing.T) {
	const (
		bitrate = "4M"
		bufsize = "8M"
		gop     = "expr:gte(t,n_forced*2)"
		scale   = "scale=-2:'min(1080,ih)'"
	)

	for _, tt := range []struct {
		name     string
		encoder  Encoder
		want     map[string]string
		absent   []string
		wantVF   []string
		vfSuffix string
		notVF    []string
	}{{
		name:    "libx264 encodes in system memory",
		encoder: libx264,
		want: map[string]string{
			"-c:v":     "libx264",
			"-preset":  "veryfast",
			"-pix_fmt": "yuv420p", // 8-bit, which is what a television decodes
		},
		absent: []string{"-init_hw_device"},
		wantVF: []string{scale},
	}, {
		name:    "h264_vaapi uploads to the GPU after a CPU scale",
		encoder: h264VAAPI,
		want: map[string]string{
			"-c:v":              "h264_vaapi",
			"-init_hw_device":   "vaapi=va:" + vaapiRenderNode,
			"-filter_hw_device": "va",
		},
		// libx264's presets are invalid for this encoder, -pix_fmt would target the
		// GPU surface rather than the frame (format=nv12 does that job), and -g is
		// VideoToolbox's workaround and means nothing here.
		absent:   []string{"-preset", "-pix_fmt", "-g"},
		wantVF:   []string{scale},
		vfSuffix: "format=nv12,hwupload",
	}, {
		name:    "h264_videotoolbox takes system-memory frames",
		encoder: h264VideoToolbox,
		want: map[string]string{
			"-c:v":     "h264_videotoolbox",
			"-pix_fmt": "yuv420p",
			// Its default GOP is sub-second, so it is lifted out of the way and
			// force_key_frames sets the real cadence.
			"-g": "600",
		},
		absent: []string{"-init_hw_device"},
		notVF:  []string{"hwupload"},
	}} {
		t.Run(tt.name, func(t *testing.T) {
			args := mustEncodeArgs(t, EncodeOptions{
				PipeFormat: SpoolFormat,
				Format:     mpegtsFormat,
				Video: EncodeVideo(VideoEncode{
					Encoder:             tt.encoder,
					Bitrate:             bitrate,
					Maxrate:             bitrate,
					Bufsize:             bufsize,
					MaxHeight:           1080,
					KeyframeIntervalSec: 2,
				}),
				Audio: aacAudio,
			})

			// The contract every encoder shares: a VBV cap the renderer's buffer can
			// live within, and a keyframe cadence a player can seek and a segmenter
			// can cut on.
			for flag, want := range map[string]string{
				"-b:v": bitrate, "-maxrate": bitrate, "-bufsize": bufsize,
				"-force_key_frames": gop,
			} {
				if got := argValue(args, flag); got != want {
					t.Errorf("%s = %q, want %q", flag, got, want)
				}
			}

			for flag, want := range tt.want {
				if got := argValue(args, flag); got != want {
					t.Errorf("%s = %q, want %q", flag, got, want)
				}
			}
			for _, flag := range tt.absent {
				if hasFlag(args, flag) {
					t.Errorf("%s must not be emitted for this encoder, got %q", flag, argValue(args, flag))
				}
			}

			vf := argValue(args, "-vf")
			for _, want := range tt.wantVF {
				if !strings.Contains(vf, want) {
					t.Errorf("-vf = %q, want it to contain %q", vf, want)
				}
			}
			if tt.vfSuffix != "" && !strings.HasSuffix(vf, tt.vfSuffix) {
				t.Errorf("-vf = %q, want it to end with %q", vf, tt.vfSuffix)
			}
			for _, not := range tt.notVF {
				if strings.Contains(vf, not) {
					t.Errorf("-vf = %q, which must not contain %q", vf, not)
				}
			}
		})
	}
}

func TestEncodeArgsSubtitlesBurnIn(t *testing.T) {
	// The planner supplies a real encoder whenever subtitles are burned in
	// (drawtext operates on decoded frames), so drawtext joins the filter chain
	// and the encoder runs.
	args, err := EncodeArgs(EncodeOptions{
		PipeFormat: SpoolFormat,
		Format:     mpegtsFormat,
		Video:      EncodeVideo(VideoEncode{Encoder: libx264, SubtitleTextFile: "/tmp/cue.txt"}),
		Audio:      aacAudio,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := argValue(args, "-c:v"); got != "libx264" {
		t.Fatalf("video codec = %q, want libx264", got)
	}
	if vf := argValue(args, "-vf"); !strings.Contains(vf, "drawtext") {
		t.Errorf("-vf = %q, want drawtext burn-in", vf)
	}
}

// TestEncodeArgsRefuses is every state EncodeArgs will not build a command line
// for. They are collected because they share a purpose: each one would otherwise
// reach ffmpeg and fail there, where the message names neither castor nor the
// decision that caused it, and two of them would not fail at all but produce
// output nothing can play.
func TestEncodeArgsRefuses(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts EncodeOptions
		// names, when set, are what the message must mention. A refusal is only
		// useful if it says what was refused.
		names []string
	}{{
		// A FormatInfo built anywhere but the registry. Its zero Framing reads as
		// "in band", which is the answer that gets a copy either killed or silently
		// destroyed, so it must be refused before a byte.
		name: "a container with no declared framing",
		opts: EncodeOptions{PipeFormat: SpoolFormat, Format: media.FormatInfo{}, Video: CopyVideo(), Audio: aacAudio},
	}, {
		name: "a container that declares a muxer but no framing",
		opts: EncodeOptions{PipeFormat: SpoolFormat, Video: CopyVideo(), Audio: aacAudio,
			Format: media.FormatInfo{ContentType: media.MP4, Muxer: media.MuxerMP4}},
	}, {
		name: "a container that declares framing but no tuning",
		opts: EncodeOptions{PipeFormat: SpoolFormat, Video: CopyVideo(), Audio: aacAudio,
			Format: media.FormatInfo{ContentType: media.MOV, Muxer: "mov", Framing: media.FramingOutOfBand}},
	}, {
		// A resolver and the carriage table disagreeing. The alternative is an
		// MPEG-TS muxer writing FLAC as private data and exiting 0 with megabytes of
		// unplayable output.
		name:  "a copy the in-band container would write as private data",
		names: []string{"flac", media.MPEGTS},
		opts: EncodeOptions{PipeFormat: SpoolFormat, Format: mpegtsFormat,
			Probe: media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: media.CodecFLAC},
			Video: CopyVideo(), Audio: CopyAudio()},
	}, {
		name:  "a copy the out-of-band container has no tag for",
		names: []string{"vp8", media.MP4},
		opts: EncodeOptions{Format: mp4Format,
			Source: NetworkSource{URL: mustURL(t, "http://example.test/in.webm"), ContentType: media.WebM},
			Probe:  media.ProbeInfo{VideoCodec: media.CodecVP8, AudioCodec: media.CodecAAC},
			Video:  CopyVideo(), Audio: CopyAudio()},
	}, {
		// An axis nobody decided must not become a stream copy. "-c:v copy" for a
		// decision that was never taken is how a track reaches a muxer nothing
		// planned for it. The guards this replaced (a burn-in on a copied bitstream,
		// an empty "-c:a") are states an EncodeOptions can no longer hold at all.
		name: "neither axis decided",
		opts: EncodeOptions{PipeFormat: SpoolFormat, Format: mpegtsFormat},
	}, {
		name: "the video axis undecided",
		opts: EncodeOptions{PipeFormat: SpoolFormat, Format: mpegtsFormat, Audio: aacAudio},
	}, {
		name: "the audio axis undecided",
		opts: EncodeOptions{PipeFormat: SpoolFormat, Format: mpegtsFormat, Video: CopyVideo()},
	}} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EncodeArgs(tt.opts)
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			for _, want := range tt.names {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
		})
	}
}

// TestSelectEncoder pins what the registry resolves to when no hardware encoder
// is available: a bogus ffmpeg path makes every hardware test-encode fail, so
// selection falls back to the software baseline registered for that codec, and
// reports it has none rather than guessing.
func TestSelectEncoder(t *testing.T) {
	for _, tt := range []struct {
		codec media.Codec
		want  string
	}{
		{media.CodecH264, "libx264"},
		{media.CodecHEVC, "libx265"},
		{media.CodecAV1, ""}, // no AV1 encoder is registered
	} {
		t.Run(string(tt.codec), func(t *testing.T) {
			enc, ok := SelectEncoder(t.Context(), "/nonexistent-ffmpeg-binary", tt.codec)
			if ok != (tt.want != "") {
				t.Fatalf("SelectEncoder(%s) ok = %v, want %v", tt.codec, ok, tt.want != "")
			}
			if enc.Name != tt.want {
				t.Errorf("SelectEncoder(%s) = %q, want %q", tt.codec, enc.Name, tt.want)
			}
		})
	}
}

func TestEncodeArgsHLSOutput(t *testing.T) {
	src, err := url.Parse("http://example.test/in.mkv")
	if err != nil {
		t.Fatal(err)
	}
	args, err := EncodeArgs(EncodeOptions{
		Source: NetworkSource{URL: src, ContentType: media.MKV},
		Format: hlsFormat,
		Video:  CopyVideo(),
		Audio:  EncodeAudio(AudioEncode{Codec: media.CodecAAC, Bitrate: "256k"}),
	})
	if err != nil {
		t.Fatal(err)
	}

	// HLS is directory output: no pipe:1, and the last token is the playlist.
	if hasFlag(args, "pipe:1") {
		t.Error("hls output must not target pipe:1")
	}
	if got := args[len(args)-1]; got != media.HLSPlaylistName {
		t.Errorf("last arg = %q, want playlist %q", got, media.HLSPlaylistName)
	}
	if got := argValue(args, "-f"); got != "hls" {
		t.Errorf("-f = %q, want hls", got)
	}
	if got := argValue(args, "-hls_segment_type"); got != "fmp4" {
		t.Errorf("-hls_segment_type = %q, want fmp4", got)
	}
	if got := argValue(args, "-hls_fmp4_init_filename"); got != media.HLSInitName {
		t.Errorf("-hls_fmp4_init_filename = %q, want %q", got, media.HLSInitName)
	}
	if got := argValue(args, "-hls_segment_filename"); got != media.HLSSegmentPattern {
		t.Errorf("-hls_segment_filename = %q, want %q", got, media.HLSSegmentPattern)
	}
	if flags := argValue(args, "-hls_flags"); !strings.Contains(flags, "delete_segments") {
		t.Errorf("-hls_flags = %q, want delete_segments for a rolling window", flags)
	}
	// playlist_type must stay unset: event/vod would pin hls_list_size to 0.
	if hasFlag(args, "-hls_playlist_type") {
		t.Error("-hls_playlist_type must be unset for a live sliding window")
	}
	// The network HLS remux MUST be paced to ~realtime: unpaced, a VOD source is
	// copied at wire speed and outruns the delete_segments window, leaving only
	// the tail fetchable. This guards that fix against regression.
	if got := argValue(args, "-readrate"); got != "1.0" {
		t.Errorf("-readrate = %q, want 1.0 so a VOD source does not outrun the sliding window", got)
	}
	if !hasFlag(args, "-readrate_initial_burst") {
		t.Error("-readrate_initial_burst must be set so the device can prebuffer one window")
	}
}

// TestEncodeArgsMP4RemuxUnpaced pins the asymmetry: a single-file network source
// is one long GET, and its mp4 remux is fronted by the replay-from-zero server
// (spooled), so it must NOT be paced: it should complete as fast as the link
// allows.
func TestEncodeArgsMP4RemuxUnpaced(t *testing.T) {
	src, err := url.Parse("http://example.test/in.mkv")
	if err != nil {
		t.Fatal(err)
	}
	args, err := EncodeArgs(EncodeOptions{
		Source: NetworkSource{URL: src, ContentType: media.MKV},
		Format: mp4Format,
		Video:  CopyVideo(),
		Audio:  aacAudio,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hasFlag(args, "-readrate") {
		t.Error("the mp4 remux of a single-file source is replay-spooled from byte 0 and must not be paced")
	}
}

// TestDemuxedSourceIsTwoInputs covers a program whose renditions live at
// separate URLs: both must be opened on the same terms, and the audio map must
// follow the audio to the second input. Mapping 0:a:0 there is the failure this
// guards, since the video rendition has no audio track and ffmpeg exits with
// reporting that the map matched no streams, before a byte is served.
func TestDemuxedSourceIsTwoInputs(t *testing.T) {
	video, err := url.Parse("http://example.test/video.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	audio, err := url.Parse("http://example.test/audio.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	source := NetworkSource{
		URL:         video,
		AudioURL:    audio,
		Headers:     http.Header{"Referer": {"https://player.example/"}},
		ContentType: media.HLS,
		Read:        vodRead,
	}

	for _, tt := range []struct {
		name string
		args []string
	}{
		{"pull", PullArgs(PullOptions{Source: source})},
		{"remux", mustEncodeArgs(t, EncodeOptions{Source: source, Format: mp4Format, Video: CopyVideo(), Audio: aacAudio})},
	} {
		name, args := tt.name, tt.args
		inputs := inputURLs(args)
		if want := []string{video.String(), audio.String()}; !slices.Equal(inputs, want) {
			t.Errorf("%s inputs = %v, want %v", name, inputs, want)
		}
		if got := argValue(args, "-map"); got != "1:a:0?" {
			t.Errorf("%s audio map = %q, want 1:a:0? (the audio is the second input)", name, got)
		}
		// Both inputs are the same origin: whatever one needs to be opened, the
		// other needs too.
		if got := countFlag(args, "-headers"); got != 2 {
			t.Errorf("%s sent headers with %d of 2 inputs", name, got)
		}
		if got := countFlag(args, "-allowed_extensions"); got != 2 {
			t.Errorf("%s relaxed extension checks on %d of 2 inputs", name, got)
		}
		if got := countFlag(args, "-readrate"); got != 2 {
			t.Errorf("%s paced %d of 2 inputs; unpaced renditions drift apart", name, got)
		}
	}
}

// TestMuxedSourceStaysOneInput is the other half: an ordinary source opens once
// and keeps mapping its own audio track.
func TestMuxedSourceStaysOneInput(t *testing.T) {
	src, err := url.Parse("http://example.test/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	args := PullArgs(PullOptions{Source: NetworkSource{URL: src, ContentType: media.HLS}})

	if got := len(inputURLs(args)); got != 1 {
		t.Errorf("inputs = %d, want 1", got)
	}
	if got := argValue(args, "-map"); got != "0:a:0?" {
		t.Errorf("audio map = %q, want 0:a:0?", got)
	}
}

// inputURLs returns the value of every -i in order.
func inputURLs(args []string) []string {
	var inputs []string
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			inputs = append(inputs, args[i+1])
		}
	}
	return inputs
}

// containsSequence reports whether want appears verbatim and contiguously inside
// args. ffmpeg cares both about the order of input options and about which input
// they precede, so a rendering that is present but scattered is not the same
// rendering.
func containsSequence(args, want []string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func countFlag(args []string, flag string) int {
	n := 0
	for _, a := range args {
		if a == flag {
			n++
		}
	}
	return n
}

func mustEncodeArgs(t *testing.T, opts EncodeOptions) []string {
	t.Helper()
	args, err := EncodeArgs(opts)
	if err != nil {
		t.Fatal(err)
	}
	return args
}

// TestEveryReadPolicyRendersItsOwnTermsInFull is the golden assertion behind the read
// table: every row is rendered in full, token for token, against the exact list castor
// opens an upstream with. The expected tokens are spelled out rather than composed from
// the helpers under test, because a golden built out of the code it is checking proves
// only that the code is self-consistent.
//
// It is the one test that must fail when a row's terms change. Everything else here
// asks whether a flag is present or what its value is, which a row that quietly
// stopped sending -reconnect_streamed would still satisfy.
//
// The source is MPEG-TS on every row, so what is asserted here is the protocol side of a
// read and only that. The segment retry budget is an option of one demuxer and renders
// against a playlist alone (see TestOnlyAPlaylistIsToldToRefetchAFailedSegment).
func TestEveryReadPolicyRendersItsOwnTermsInFull(t *testing.T) {
	// The reconnect block every shipped row carries: a minute of backoff paired with the
	// transient status class, applied to the input that follows it. The mid-read deadline is
	// NOT part of it, because one row withholds that and shares all of this.
	reconnect := []string{
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "60",
		"-reconnect_on_http_error", "429,500,502,503,504",
	}
	// The configured thirty seconds, in the microseconds -rw_timeout takes.
	deadline := []string{"-rw_timeout", "30000000"}
	url := "http://example.test/in.ts"

	for _, tt := range []struct {
		name   string
		policy read.Policy
		want   []string
	}{{
		name:   "a segmented VOD source runs ahead of playback after a wire-speed burst",
		policy: vodRead,
		want:   slices.Concat([]string{"-readrate", "2.0", "-readrate_initial_burst", "90"}, deadline, reconnect),
	}, {
		// The whole of what arming the fragile row does to a command line: this list is the
		// one above with the deadline gone. A -rw_timeout that fires partway through an fMP4
		// fragment truncates it, and the truncated AVCC stream kills the reader at exit 183
		// on "Invalid NAL unit size".
		name:   "fMP4 segments are read with no mid-read deadline at all",
		policy: fragileRead,
		want:   slices.Concat([]string{"-readrate", "2.0", "-readrate_initial_burst", "90"}, reconnect),
	}, {
		// A live fMP4 edge is answered by this row rather than the fragile one, and it keeps
		// the deadline: the read holds no lead to spend waiting, and the fragment it would
		// wait for rolls out of the live window.
		name:   "a live edge is read at wall-clock speed with no burst",
		policy: liveRead,
		want:   slices.Concat([]string{"-readrate", "1.0", "-readrate_initial_burst", "0"}, deadline, reconnect),
	}, {
		// The row the configured duration was written for, unchanged: one long GET where a
		// stalled read is the only symptom a tarpit has.
		name:   "one long GET is paced like any other VOD read",
		policy: longGETRead,
		want:   slices.Concat([]string{"-readrate", "2.0", "-readrate_initial_burst", "90"}, deadline, reconnect),
	}} {
		t.Run(tt.name, func(t *testing.T) {
			src := NetworkSource{URL: mustURL(t, url), ContentType: media.MPEGTS, Read: tt.policy}
			want := append(slices.Clone(tt.want), "-i", url)
			if got := src.inputArgs(tt.policy.Pace); !slices.Equal(got, want) {
				t.Errorf("policy %q renders\n%q\nwant\n%q", tt.policy.Name, got, want)
			}
		})
	}
}

// TestTheReadTermsPrecedeTheInputTheyApplyTo pins the ordering the flags above are
// useless without. ffmpeg applies an input option to the input that FOLLOWS it, so
// the pace, the read terms, the request headers and the container's leniency flags
// all have to be emitted ahead of the -i they belong to, and a demuxed program has
// to repeat every one of them for its second input.
func TestTheReadTermsPrecedeTheInputTheyApplyTo(t *testing.T) {
	video, audio := "http://example.test/v.m3u8", "http://example.test/a.m3u8"
	src := NetworkSource{
		URL:         mustURL(t, video),
		AudioURL:    mustURL(t, audio),
		Headers:     http.Header{"Referer": {"https://player.example/"}},
		ContentType: media.HLS,
		Read:        vodRead,
	}

	// One input's worth of flags: pace, read terms, headers, container leniency plus the
	// segment retry budget that leniency's demuxer owns, URL.
	perInput := slices.Concat([]string{
		"-readrate", "2.0", "-readrate_initial_burst", "90",
		"-rw_timeout", "30000000",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "60",
		"-reconnect_on_http_error", "429,500,502,503,504",
		"-headers", "Referer: https://player.example/\r\n",
	}, media.HLSInputArgs, []string{"-seg_max_retry", "3"})

	args := src.inputArgs(vodRead.Pace)
	want := slices.Concat(perInput, []string{"-i", video}, perInput, []string{"-i", audio})
	if !slices.Equal(args, want) {
		t.Errorf("a demuxed read renders\n%q\nwant\n%q", args, want)
	}
}

// TestNetworkReadersShareInputPolicy pins the invariant behind NetworkSource:
// castor's two network readers open the same upstream on the same terms.
//
// It asserts the POLICY VALUE both readers carry and not just that their two flag
// lists agree, because agreeing lists is the weaker claim: two readers could each
// build the same eight literals from different reasoning and drift the moment one
// of them grew a condition. One value cannot drift from itself, and the flags are
// then a rendering of it, which is what the second half checks.
func TestNetworkReadersShareInputPolicy(t *testing.T) {
	src, err := url.Parse("http://example.test/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	source := NetworkSource{
		URL:         src,
		Headers:     http.Header{"Referer": {"https://player.example/"}},
		ContentType: media.HLS,
		Read:        vodRead,
	}

	remux, err := EncodeArgs(EncodeOptions{Source: source, Format: mp4Format, Video: CopyVideo(), Audio: aacAudio})
	if err != nil {
		t.Fatal(err)
	}
	pull := PullArgs(PullOptions{Source: source})

	// The rendering of the ONE policy the source carries, which each reader's command
	// line must contain verbatim and contiguously. This is the strong form of the
	// claim: comparing the two readers to each other passes as soon as they agree,
	// including when both agree on terms neither the source nor the read table chose.
	terms := source.inputArgs(source.Read.Pace)
	for _, reader := range []struct {
		name string
		args []string
	}{{"remux", remux}, {"pull", pull}} {
		if !containsSequence(reader.args, terms) {
			t.Errorf("the %s does not open the upstream on the source's own terms\ngot  %q\nwant %q somewhere in it", reader.name, reader.args, terms)
		}
	}

	for _, flag := range []string{
		"-rw_timeout", "-reconnect", "-reconnect_streamed",
		"-reconnect_delay_max", "-reconnect_on_http_error",
		"-headers", "-allowed_extensions", "-extension_picky",
		"-readrate", "-readrate_initial_burst", "-seg_max_retry",
	} {
		if got, want := argValue(remux, flag), argValue(pull, flag); got != want {
			t.Errorf("%s: remux = %q, pull = %q; both read the same upstream and must agree", flag, got, want)
		}
	}
}

// TestOnlyAPlaylistIsToldToRefetchAFailedSegment pins where the segment retry budget may be
// rendered, and the answer is decided by the demuxer that owns the option rather than by the
// policy that carries it.
//
// Getting it wrong is not a flag quietly ignored. "-seg_max_retry 3 -i in.mp4" makes ffmpeg
// print "Option seg_max_retry not found." and exit before opening the file, so a budget
// rendered against a direct source would refuse every cast of one. The read table already
// withholds it from the row that answers a source no playlist described, and this is the
// second half of that: whatever the policy says, the rendering asks what castor is about to
// open.
func TestOnlyAPlaylistIsToldToRefetchAFailedSegment(t *testing.T) {
	url := mustURL(t, "http://example.test/in")
	budget := []string{"-seg_max_retry", "3"}

	for _, tt := range []struct {
		name        string
		contentType string
		policy      read.Policy
		want        bool
	}{{
		name:        "a playlist is asked to re-fetch a segment whose open failed",
		contentType: media.HLS,
		policy:      fragileRead,
		want:        true,
	}, {
		name:        "so is a playlist of MPEG-TS segments, where a failed open is the same event",
		contentType: media.HLS,
		policy:      vodRead,
		want:        true,
	}, {
		name:        "a direct file is not, because the option is one its demuxer aborts on",
		contentType: media.MP4,
		policy:      fragileRead,
		want:        false,
	}, {
		name:        "nor is a raw MPEG-TS stream, whichever policy is carrying a budget",
		contentType: media.MPEGTS,
		policy:      vodRead,
		want:        false,
	}, {
		name:        "and a policy with no budget renders none even against a playlist",
		contentType: media.HLS,
		policy:      longGETRead,
		want:        false,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			src := NetworkSource{URL: url, ContentType: tt.contentType, Read: tt.policy}
			args := src.inputArgs(tt.policy.Pace)
			if got := containsSequence(args, budget); got != tt.want {
				t.Errorf("policy %q against a %s source renders %q; -seg_max_retry present = %t, want %t",
					tt.policy.Name, tt.contentType, args, got, tt.want)
			}
		})
	}
}

// TestEncodeArgsHLSSourcePaced pins the other half of that rule: an HLS source is
// fetched segment by segment from a rate-limiting CDN, so even the replay-spooled
// mp4 remux paces its upstream read exactly like the puller does, rather than
// pulling a whole movie as a segment burst and earning a 429. Which pace that is
// comes from the source's read policy, so a remux and a pull of one source cannot
// disagree about how fast it may be consumed.
func TestEncodeArgsHLSSourcePaced(t *testing.T) {
	src, err := url.Parse("http://example.test/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	base := EncodeOptions{
		Source: NetworkSource{URL: src, ContentType: media.HLS, Read: vodRead},
		Format: mp4Format,
		Video:  CopyVideo(),
		Audio:  aacAudio,
	}

	args, err := EncodeArgs(base)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := argValue(args, "-readrate"), formatRate(vodRead.Pace.Realtime); got != want {
		t.Errorf("VOD readrate = %q, want %q (ahead of 1x playback, but not a wire-speed storm)", got, want)
	}
	if got, want := argValue(args, "-readrate_initial_burst"), "90"; got != want {
		t.Errorf("VOD burst = %q, want %q", got, want)
	}

	base.Source.Read = liveRead
	args, err = EncodeArgs(base)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := argValue(args, "-readrate"), formatRate(liveRead.Pace.Realtime); got != want {
		t.Errorf("live readrate = %q, want %q (a live source cannot be outrun)", got, want)
	}
	if got, want := argValue(args, "-readrate_initial_burst"), "0"; got != want {
		t.Errorf("live burst = %q, want %q", got, want)
	}
}
