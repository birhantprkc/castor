package core

import (
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

// TestSelectVideoEncoder pins the re-encode codec ladder: prefer the most
// efficient codec the renderer advertises AND this host can hardware-encode,
// never live-software-HEVC, and always fall to H.264. It hoisted from the old
// DLNA strategy test alongside selectVideoEncoder itself; the fake selectEncoder
// keeps it host-independent.
func TestSelectVideoEncoder(t *testing.T) {
	renderer := func(codecs ...media.Codec) media.Renderer {
		var r media.Renderer
		for _, c := range codecs {
			r.Video = append(r.Video, media.VideoSupport{Codec: c})
		}
		return r
	}
	fixed := func(m map[media.Codec]ffmpeg.Encoder) func(media.Codec) (ffmpeg.Encoder, bool) {
		return func(c media.Codec) (ffmpeg.Encoder, bool) { e, ok := m[c]; return e, ok }
	}

	hevcHW := ffmpeg.Encoder{Name: "hevc_videotoolbox", Codec: media.CodecHEVC, Hardware: true}
	hevcSW := ffmpeg.Encoder{Name: "libx265", Codec: media.CodecHEVC}
	h264HW := ffmpeg.Encoder{Name: "h264_videotoolbox", Codec: media.CodecH264, Hardware: true}
	h264SW := ffmpeg.Encoder{Name: "libx264", Codec: media.CodecH264}

	tests := []struct {
		name  string
		caps  media.Renderer
		avail map[media.Codec]ffmpeg.Encoder
		want  string
	}{
		{
			name:  "HEVC renderer with hardware HEVC picks HEVC",
			caps:  renderer(media.CodecHEVC, media.CodecH264),
			avail: map[media.Codec]ffmpeg.Encoder{media.CodecHEVC: hevcHW, media.CodecH264: h264HW},
			want:  "hevc_videotoolbox",
		},
		{
			name:  "HEVC renderer with only software HEVC falls to H.264 (never software HEVC live)",
			caps:  renderer(media.CodecHEVC, media.CodecH264),
			avail: map[media.Codec]ffmpeg.Encoder{media.CodecHEVC: hevcSW, media.CodecH264: h264HW},
			want:  "h264_videotoolbox",
		},
		{
			name:  "H.264-only renderer uses H.264 even when hardware HEVC exists",
			caps:  renderer(media.CodecH264),
			avail: map[media.Codec]ffmpeg.Encoder{media.CodecHEVC: hevcHW, media.CodecH264: h264SW},
			want:  "libx264",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectVideoEncoder(tt.caps, fixed(tt.avail)); got.Name != tt.want {
				t.Errorf("selectVideoEncoder = %q, want %q", got.Name, tt.want)
			}
		})
	}
}

// TestPreferredVideoCodecsHaveTargets guards the coupling between the codec
// ladder and the bitrate map: a preferred codec with no target would transcode
// unbounded (no -maxrate), silently reintroducing the rebuffering these fix.
func TestPreferredVideoCodecsHaveTargets(t *testing.T) {
	for _, c := range codecPreference {
		if _, ok := videoTargets[c]; !ok {
			t.Errorf("codec %q is in codecPreference but has no videoTargets entry", c)
		}
	}
}

// TestTheFloorCodecsBudgetIsTheOneBothProducersRead pins the relationship rather than
// the number, at the end where a divergence would be written. Two producers emit the floor
// codec (this layer when nothing better survives, and the read-once pull when a copy into
// the buffer is refused or has already broken), and one of them used to emit it with no
// ceiling at all. A pull aiming at a different budget than the encode downstream of it
// spends the quality twice for nothing.
func TestTheFloorCodecsBudgetIsTheOneBothProducersRead(t *testing.T) {
	target, ok := videoTargets[media.FloorVideoCodec]
	if !ok {
		t.Fatalf("the floor codec %q has no re-encode target", media.FloorVideoCodec)
	}
	if target.bitrate != media.FloorVideoBitrate || target.maxrate != media.FloorVideoMaxrate || target.bufsize != media.FloorVideoBufsize {
		t.Errorf("the floor codec's target here is %+v while the pull emits %s/%s/%s: one budget, read from one place",
			target, media.FloorVideoBitrate, media.FloorVideoMaxrate, media.FloorVideoBufsize)
	}
}

// TestDecideVideo pins the copy-vs-encode answer the served paths run against a
// probe: stream-copy only when nothing forces a re-encode, else a re-encode to a
// VBV-capped target carrying this leg's ceiling and GOP bound. It is the
// whole-function counterpart to TestSelectVideoEncoder (which pins only the codec
// ladder), and heir to the copy-vs-encode assertions the deleted DLNA pipeline
// test carried.
//
// The re-encode rows resolve a concrete encoder through the real SelectEncoder
// (DecideVideo wires it directly, not through the injectable seam), which proves
// each candidate with a one-frame test encode, so those rows need a working
// ffmpeg and skip without one. The copy rows are pure and always run.
func TestDecideVideo(t *testing.T) {
	// A renderer that natively decodes 8-bit H.264: nil Profiles means "any
	// profile" and nil BitDepths means "8-bit", so the copyable source below
	// matches and is stream-copy eligible.
	h264Renderer := media.Renderer{Video: []media.VideoSupport{{Codec: media.CodecH264}}}
	// A copyable probed source: 8-bit H.264, short enough for the height ceilings
	// used below. BitDepth must be set: an unknown depth (0) is not in {8} and so
	// would itself defeat the copy, masking the axis each row means to isolate.
	copyable := media.ProbeInfo{VideoCodec: media.CodecH264, VideoHeight: 720, VideoBitDepth: 8}
	mpegtsFormat := testFormat(t, media.MPEGTS)
	mp4Format := testFormat(t, media.MP4)

	t.Run("copyable source within the height cap is stream-copied", func(t *testing.T) {
		track := DecideVideo(t.Context(), VideoInputs{
			Caps: h264Renderer, Probe: copyable, Into: mpegtsFormat,
			Policy: CopyWhatFits, MaxHeight: 1080,
		})
		if enc, ok := track.Encode(); ok {
			t.Fatalf("expected stream-copy, got a re-encode to %q", enc.Encoder.Name)
		}
		if !track.Decided() {
			t.Error("a copy must be a decision, not the zero value: nothing downstream can tell them apart otherwise")
		}
	})

	// Each row flips exactly one input that must force the re-encode branch,
	// isolating that trigger: the height gate, the burn-in signal, an undecodable
	// source codec, and a refusal an artifact reported after the fact.
	reencode := []struct {
		name   string
		caps   media.Renderer
		src    media.ProbeInfo
		maxH   media.HeightCap
		burnIn string
	}{
		{name: "a source above the height cap re-encodes", caps: h264Renderer, src: copyable, maxH: 480},
		{name: "a burn-in forces a re-encode even when the source is copyable", caps: h264Renderer, src: copyable, maxH: 1080, burnIn: "/tmp/cue.txt"},
		{name: "a codec the renderer cannot decode re-encodes", caps: h264Renderer, src: media.ProbeInfo{VideoCodec: media.CodecHEVC, VideoHeight: 720, VideoBitDepth: 8}, maxH: 1080},
	}
	for _, tt := range reencode {
		t.Run(tt.name, func(t *testing.T) {
			track := DecideVideo(t.Context(), VideoInputs{
				Caps: tt.caps, Probe: tt.src, Into: mpegtsFormat,
				Policy: CopyWhatFits, MaxHeight: tt.maxH, GOPSeconds: 2,
				BurnIn:     tt.burnIn,
				FFmpegPath: requireFFmpeg(t),
			})
			enc, ok := track.Encode()
			if !ok {
				t.Fatal("expected a re-encode, got stream-copy")
			}
			// The chosen encoder's codec must carry its VBV-capped target, or the
			// transcode would run unbounded. Which concrete encoder wins is
			// host-dependent (hardware vs software), so assert against the target
			// for the codec it produced rather than a fixed bitrate.
			target, ok := videoTargets[enc.Encoder.Codec]
			if !ok {
				t.Fatalf("encoder %q produces codec %q with no videoTargets entry", enc.Encoder.Name, enc.Encoder.Codec)
			}
			if enc.Bitrate != target.bitrate {
				t.Errorf("video bitrate = %q, want %q (the %q target)", enc.Bitrate, target.bitrate, enc.Encoder.Codec)
			}
			// The invariant a partial constructor used to be able to break: a
			// re-encode always carries the ceiling and the GOP bound of the leg that
			// asked for it, because there is no way to build one without them.
			if enc.MaxHeight != tt.maxH {
				t.Errorf("re-encode MaxHeight = %d, want the leg's ceiling %d", enc.MaxHeight, tt.maxH)
			}
			if enc.KeyframeIntervalSec != 2 {
				t.Errorf("re-encode KeyframeIntervalSec = %d, want the leg's GOP bound 2", enc.KeyframeIntervalSec)
			}
			if enc.SubtitleTextFile != tt.burnIn {
				t.Errorf("re-encode burn-in = %q, want %q", enc.SubtitleTextFile, tt.burnIn)
			}
		})
	}

	// The container question. It is not a capability gate and adds no ceiling: it
	// asks only whether ffmpeg's muxer has a stream type for the codec. The answer
	// -f mpegts writes VP9 as private data at exit 0 with "150 packets muxed" and
	// no video stream at all in the output, so a renderer that decodes VP9 would
	// still have been handed nothing. Re-encoding into the identical flag set gave
	// exit 0 and 150/150 decoded frames.
	t.Run("a copy the output container cannot carry re-encodes", func(t *testing.T) {
		vp9Renderer := media.Renderer{Video: []media.VideoSupport{{Codec: media.CodecVP9}}}
		vp9 := media.ProbeInfo{VideoCodec: media.CodecVP9, VideoHeight: 720, VideoBitDepth: 8}
		in := VideoInputs{
			Caps: vp9Renderer, Probe: vp9, Into: mpegtsFormat,
			Policy: CopyWhatFits, MaxHeight: 1080, FFmpegPath: requireFFmpeg(t),
		}
		if _, ok := DecideVideo(t.Context(), in).Encode(); !ok {
			t.Fatal("expected a re-encode: MPEG-TS writes VP9 as private data at exit 0 with no video stream in the output")
		}

		// Per-destination, not a codec ban: the same source and the same renderer
		// copy into fragmented mp4, which carries VP9 intact.
		in.Into = mp4Format
		if enc, ok := DecideVideo(t.Context(), in).Encode(); ok {
			t.Errorf("expected a stream-copy into mp4, got %q: the check is per (codec, destination), not a codec ban", enc.Encoder.Name)
		}
	})

	// The two policies, which are the only thing that differs between the served
	// shapes, and the exact width of what they differ IN. A remux changes the wrapper
	// and not the picture, so it copies a bitstream this renderer never advertised;
	// what it does not lift is anything that was never a judgement about the renderer.
	t.Run("CopyWhatever copies past the renderer's envelope and nothing else", func(t *testing.T) {
		// A codec the renderer advertises no support for, inside the ceiling and carriable
		// by the destination: the capability gate is the only thing left to answer.
		undeclared := media.ProbeInfo{VideoCodec: media.CodecHEVC, VideoHeight: 720, VideoBitDepth: 8}
		in := VideoInputs{
			Caps: h264Renderer, Probe: undeclared, Into: mpegtsFormat,
			Policy: CopyWhatever, MaxHeight: 1080, FFmpegPath: requireFFmpeg(t),
		}
		if enc, ok := DecideVideo(t.Context(), in).Encode(); ok {
			t.Errorf("expected a stream-copy, got %q: a remux hands over the envelope the source published, and a renderer listing h264 alone routinely decodes it", enc.Encoder.Name)
		}

		in.Probe = media.ProbeInfo{VideoCodec: media.CodecVP9, VideoHeight: 720, VideoBitDepth: 8}
		if _, ok := DecideVideo(t.Context(), in).Encode(); !ok {
			t.Error("expected a re-encode: MPEG-TS cannot carry VP9 whatever the policy says")
		}
	})

	// TestDecideVideo's ceiling row above proves it under CopyWhatFits; this is the half
	// that used to be missing, and its absence is what let one delivery path honour
	// max_height while the other ignored it.
	//
	// The two legs are reached by discovery: a renderer that fetches for itself and takes
	// the container gets a pass-through, one that fetches but rejects the container gets a
	// remux, one that never fetches gets the buffer. Nothing tells a user which of those
	// happened, so a ceiling that only bound the third meant a 1080p-capped cast of a 4K
	// source delivered 4K, at full source bitrate, with no line anywhere saying why.
	//
	// The source here is one the permissive policy would otherwise copy on every other
	// ground: an envelope this renderer does advertise, in a container that carries it.
	t.Run("the height ceiling binds on every policy", func(t *testing.T) {
		tall := media.ProbeInfo{VideoCodec: media.CodecH264, VideoHeight: 2160, VideoBitDepth: 8}
		for _, policy := range []VideoPolicy{CopyWhatFits, CopyWhatever} {
			track := DecideVideo(t.Context(), VideoInputs{
				Caps: h264Renderer, Probe: tall, Into: mpegtsFormat,
				Policy: policy, MaxHeight: 1080, FFmpegPath: requireFFmpeg(t),
			})
			enc, ok := track.Encode()
			if !ok {
				t.Errorf("under %v a 2160p source was copied under a 1080 ceiling: no policy value may short-circuit what the user asked for", policy)
				continue
			}
			// And the re-encode has to actually carry it, or the ceiling has only cost the
			// cast an encode and delivered the same 2160p picture out the far side.
			if enc.MaxHeight != 1080 {
				t.Errorf("under %v the forced re-encode carries MaxHeight %d, want the cast's 1080", policy, enc.MaxHeight)
			}
		}
	})

	// The one piece of evidence that outranks a policy, and it has to: CopyWhatever
	// copies whatever the source is precisely BECAUSE nothing is known against the
	// bitstream, and a reader of this very cast having exited on those packets is that
	// knowledge. Without this the buffered leg would decode a truncated fragment and the
	// encode reading its output would copy the same packets straight back out, and the
	// remux leg would ignore the evidence entirely.
	t.Run("an axis a previous attempt's copy broke on is never copied again", func(t *testing.T) {
		for _, policy := range []VideoPolicy{CopyWhatFits, CopyWhatever} {
			in := VideoInputs{
				Caps: h264Renderer, Probe: copyable, Into: mpegtsFormat,
				Policy: policy, MaxHeight: 1080,
				Decode:     carriage.Axes{Video: true},
				FFmpegPath: requireFFmpeg(t),
			}
			if _, ok := DecideVideo(t.Context(), in).Encode(); !ok {
				t.Errorf("a source the reader already died copying was copied again under %v", policy)
			}
			// The blame is per axis: a video the evidence never mentioned is still copied,
			// because re-encoding a track nobody implicated spends quality for nothing.
			in.Decode = carriage.Axes{Audio: true}
			if enc, ok := DecideVideo(t.Context(), in).Encode(); ok {
				t.Errorf("under %v a blamed AUDIO axis re-encoded the video to %q as well", policy, enc.Encoder.Name)
			}
		}
	})

	// The regression guard for the scope limit: everything that copied before must
	// still copy. h264 and hevc are carried by all three containers castor
	// produces, so no adaptation may block them anywhere.
	t.Run("every envelope that copied before still copies", func(t *testing.T) {
		probes := map[media.Codec]media.ProbeInfo{
			media.CodecH264: {VideoCodec: media.CodecH264, VideoHeight: 720, VideoBitDepth: 8},
			media.CodecHEVC: {VideoCodec: media.CodecHEVC, VideoHeight: 720, VideoBitDepth: 8},
		}
		for codec, probe := range probes {
			caps := media.Renderer{Video: []media.VideoSupport{{Codec: codec}}}
			for _, ct := range []string{media.MPEGTS, media.MP4, media.HLS} {
				track := DecideVideo(t.Context(), VideoInputs{
					Caps: caps, Probe: probe, Into: testFormat(t, ct),
					Policy: CopyWhatFits, MaxHeight: 1080,
				})
				if enc, ok := track.Encode(); ok {
					t.Errorf("%s into %s re-encoded to %q; it copied before this change and must still copy",
						codec, ct, enc.Encoder.Name)
				}
			}
		}
	})
}

// TestTheCeilingsForcedTranscodeIsAttributable pins the line, because the line is half the
// change. The ceiling now buys a decode, a scale and a re-encode on casts that used to copy,
// and that work has to hold realtime for the whole title: a hardware encoder does it
// comfortably, a software one on a busy host measures well under 1.0x (the deliverability
// rule's calibration runs 0.0627x to 0.39x). Nothing downstream will attribute it, and
// deliberately so: no deliverability verdict is reached on an encode castor chose to run, so a
// user watching a 4K source stutter under a 1080 ceiling has this line or nothing.
//
// The four terms are each load-bearing. Without the two heights the cost cannot be traced to
// the ceiling rather than to a codec the renderer refused; without the encoder and its
// hardware flag it cannot be traced to this host, which is the half a user can actually
// change (raise the ceiling, or cast from a machine with an encoder).
func TestTheCeilingsForcedTranscodeIsAttributable(t *testing.T) {
	lines := captureLog(t)
	track := DecideVideo(t.Context(), VideoInputs{
		Caps:   media.Renderer{Video: []media.VideoSupport{{Codec: media.CodecH264}}},
		Probe:  media.ProbeInfo{VideoCodec: media.CodecH264, VideoHeight: 2160, VideoBitDepth: 8},
		Into:   testFormat(t, media.MPEGTS),
		Policy: CopyWhatever, MaxHeight: 1080, FFmpegPath: requireFFmpeg(t),
	})
	enc, ok := track.Encode()
	if !ok {
		t.Fatal("a 2160p source under a 1080 ceiling was copied, so there is no cost to attribute")
	}

	got, found := lines.find("height ceiling")
	if !found {
		t.Fatalf("the ceiling forced a transcode to %q and said nothing about it: %v", enc.Encoder.Name, lines.all())
	}
	for _, want := range []string{
		"source_height=2160",
		"max_height=1080",
		"encoder=" + enc.Encoder.Name,
		fmt.Sprintf("hardware=%v", enc.Encoder.Hardware),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the forced-transcode line %q does not carry %q", got, want)
		}
	}
}

// TestTheCeilingIsSilentWhenItCostsNothing is the other half: the line is an attribution of a
// cost, so a re-encode nobody's ceiling forced must not be blamed on one. A cast whose source
// already fits is the common case, and a line claiming the ceiling made it transcode would send
// a user to raise a ceiling that was never in the way.
func TestTheCeilingIsSilentWhenItCostsNothing(t *testing.T) {
	lines := captureLog(t)
	// Under the cap in height, refused for an entirely different reason: a codec the
	// renderer never advertised.
	DecideVideo(t.Context(), VideoInputs{
		Caps:   media.Renderer{Video: []media.VideoSupport{{Codec: media.CodecH264}}},
		Probe:  media.ProbeInfo{VideoCodec: media.CodecHEVC, VideoHeight: 720, VideoBitDepth: 8},
		Into:   testFormat(t, media.MPEGTS),
		Policy: CopyWhatFits, MaxHeight: 1080, FFmpegPath: requireFFmpeg(t),
	})
	if got, found := lines.find("height ceiling"); found {
		t.Errorf("a re-encode the ceiling had no part in was attributed to it: %q", got)
	}
}

// logLines collects what a decision said about itself for one test. The log is not a shortcut
// around the API here: a forced transcode has no return value to inspect (a re-encode forced by
// the ceiling and one forced by a codec produce the identical track), so the line IS the
// statement, and it is the statement production makes.
type logLines struct {
	mu    sync.Mutex
	lines []string
}

func captureLog(t *testing.T) *logLines {
	t.Helper()
	l := &logLines{}
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(l, nil)))
	t.Cleanup(func() { slog.SetDefault(restore) })
	return l
}

func (l *logLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, string(p))
	return len(p), nil
}

// find answers with the one line mentioning substr, so a test names what it is looking for
// rather than an index into whatever else the decision logged.
func (l *logLines) find(substr string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			return line, true
		}
	}
	return "", false
}

func (l *logLines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lines)
}

// requireFFmpeg returns the ffmpeg path or skips: the re-encode branch of
// DecideVideo resolves a real encoder (SelectEncoder runs a test encode), so a
// host without ffmpeg cannot exercise it.
func requireFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH; skipping the re-encode branch (encoder selection runs a real test encode)")
	}
	return path
}
