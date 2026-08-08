package carriage

import (
	"testing"

	"github.com/stupside/castor/internal/media"
)

func format(t *testing.T, contentType string) media.FormatInfo {
	t.Helper()
	f, ok := media.FormatForContentType(contentType)
	if !ok {
		t.Fatalf("castor cannot produce %q", contentType)
	}
	return f
}

// TestKnownMatrix pins every pair castor has been told about, and every pair it
// has been told is fine. The second half matters as much as the first: a codec
// absent from the rules has to mean "nothing known against it", never "never
// considered", because the only thing standing between those two readings is that
// the muxer gets the final say (see TestVerdictReadsTheArtifact).
func TestKnownMatrix(t *testing.T) {
	mpegts, mp4, hls := format(t, media.MPEGTS), format(t, media.MP4), format(t, media.HLS)

	audio := []struct {
		codec   media.Codec
		into    media.FormatInfo
		refused bool
	}{
		// AAC travels everywhere; what it needs to survive the trip is an adaptation,
		// not a carriage question.
		{media.CodecAAC, mpegts, false},
		{media.CodecAAC, mp4, false},
		{media.CodecAAC, hls, false},

		// aac_latm is a different codec id, and the mp4 family has no tag for it. No
		// LATM to ASC filter exists either, so there is nothing to adapt.
		{media.CodecAACLATM, mpegts, false},
		{media.CodecAACLATM, mp4, true},
		{media.CodecAACLATM, hls, true},

		// Dolby is carried by all three. What mp4 needs is a delayed header.
		{media.CodecAC3, mpegts, false},
		{media.CodecAC3, mp4, false},
		{media.CodecEAC3, mp4, false},

		// MPEG-TS has no stream type for these and does not say so: it writes them as
		// private data and exits cleanly, so the output has no audio at all.
		{media.CodecFLAC, mpegts, true},
		{media.CodecFLAC, mp4, false},
		{media.CodecVorbis, mpegts, true},
		{media.CodecVorbis, mp4, false},
		{media.Codec("pcm_s16le"), mpegts, true},
		{media.Codec("pcm_s16le"), mp4, false},

		// Companded PCM is refused by both, which is why carriage cannot be reduced to
		// a property of the codec or of the container alone.
		{media.Codec("pcm_mulaw"), mpegts, true},
		{media.Codec("pcm_mulaw"), mp4, true},
		{media.CodecWMAv2, mpegts, false},
		{media.CodecWMAv2, mp4, true},

		// Measured to need nothing anywhere. Absent rules, present rows.
		{media.CodecMP3, mpegts, false},
		{media.CodecMP3, mp4, false},
		{media.CodecOpus, mpegts, false},
		{media.CodecOpus, mp4, false},
		{media.CodecDTS, mpegts, false},

		// No track is not a refusal.
		{"", mpegts, false},
		{"", mp4, false},
	}
	for _, tt := range audio {
		probe := media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: tt.codec}
		if got := Known(probe, tt.into).Audio; got != tt.refused {
			t.Errorf("Known(audio %q -> %s).Audio = %v, want %v", tt.codec, tt.into.ContentType, got, tt.refused)
		}
	}

	video := []struct {
		codec   media.Codec
		into    media.FormatInfo
		refused bool
	}{
		{media.CodecH264, mpegts, false},
		{media.CodecH264, mp4, false},
		{media.CodecHEVC, mpegts, false},
		{media.CodecHEVC, mp4, false},

		// VP9, AV1 and MJPEG have mp4 tags and no MPEG-TS stream type; VP8 and
		// msmpeg4v3 have neither. Neither broken set contains the other, which is the
		// reason this is a table of measurements rather than a rule.
		{media.CodecVP8, mpegts, true},
		{media.CodecVP8, mp4, true},
		{media.CodecVP9, mpegts, true},
		{media.CodecVP9, mp4, false},
		{media.CodecAV1, mpegts, true},
		{media.CodecAV1, mp4, false},
		{media.CodecMJPEG, mpegts, true},
		{media.CodecMJPEG, mp4, false},
		{media.CodecMSMPEG4v3, mpegts, true},
		{media.CodecMSMPEG4v3, mp4, true},

		// mpeg4 works where msmpeg4v3 does not, which is why the set cannot be
		// guessed from how old or how modern a codec is.
		{media.CodecMPEG4, mpegts, false},
		{media.CodecMPEG2, mpegts, false},

		// The older web and broadcast codecs the mp4 family has no tag for. They are
		// rows rather than a runtime discovery because the table IS the mechanism:
		// castor knows these fail, so it re-encodes them without trying first.
		{media.Codec("theora"), mp4, true},
		{media.Codec("theora"), mpegts, false},
		{media.Codec("flv1"), mp4, true},
		{media.Codec("wmv2"), mp4, true},
		{media.Codec("h263"), mp4, true},

		{"", mpegts, false},
	}
	for _, tt := range video {
		probe := media.ProbeInfo{VideoCodec: tt.codec, AudioCodec: media.CodecAAC}
		if got := Known(probe, tt.into).Video; got != tt.refused {
			t.Errorf("Known(video %q -> %s).Video = %v, want %v", tt.codec, tt.into.ContentType, got, tt.refused)
		}
	}
}

// TestKnownTrueHDNeedsVideo covers the one rule no artifact can reveal: the
// output is exit 0 with a full packet count and audio that does not decode, and
// ffmpeg prints nothing at all about it. It has to be known because it cannot be
// seen.
func TestKnownTrueHDNeedsVideo(t *testing.T) {
	mp4 := format(t, media.MP4)

	alone := media.ProbeInfo{AudioCodec: media.CodecTrueHD}
	if !Known(alone, mp4).Audio {
		t.Error("TrueHD alone in a fragmented mp4 must not be copied: its samples are sliced at the wrong offset")
	}
	withVideo := media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: media.CodecTrueHD}
	if Known(withVideo, mp4).Audio {
		t.Error("TrueHD alongside video is correct and must not be re-encoded")
	}
}

// TestNoProseIsParsed is the rule this package exists to keep. Nothing here may
// take an ffmpeg message as input: the CLI has no machine-readable error channel,
// so a strategy keyed on its wording is a contract nobody offered and one that
// breaks on an upgrade with no test failing. Known's whole input is a probe and a
// destination, and this asserts that by construction.
func TestNoProseIsParsed(t *testing.T) {
	var _ func(media.ProbeInfo, media.FormatInfo) Axes = Known
}

// TestCopyingIsTheHalvesTheReaderPassesThrough pins the one place this package answers a
// question about a FAILURE rather than about a muxer, and it is the complement rather than
// a second opinion.
//
// It matters because it decides what a recovery is aimed at. Only a passed-through
// bitstream can die on a filter it was handed (a produced axis is produced to the floor,
// which every container castor writes carries by definition), so reading these the wrong
// way round would blame the half castor had just re-encoded and leave the half that broke
// being copied again.
func TestCopyingIsTheHalvesTheReaderPassesThrough(t *testing.T) {
	if got := (Axes{}).Copying(); !got.Video || !got.Audio {
		t.Errorf("a reader asked to re-encode nothing is copying %s, want both halves", got)
	}
	if got := (Axes{Video: true}).Copying(); got.Video || !got.Audio {
		t.Errorf("a reader producing the video is copying %s, want the audio alone", got)
	}
	if got := (Axes{Video: true, Audio: true}).Copying(); got.Any() {
		t.Errorf("a reader producing both halves is copying %s, want nothing to blame", got)
	}
}

// TestOrKeepsEveryDemandToReEncode covers the union two independent facts arrive as: what
// this container is known not to carry, and what a previous attempt's copy broke on.
// Neither overrules the other, so an axis either of them names is decoded.
func TestOrKeepsEveryDemandToReEncode(t *testing.T) {
	if got := (Axes{Video: true}).Or(Axes{Audio: true}); !got.Video || !got.Audio {
		t.Errorf("Or = %s, want both halves: each demand came from a different fact", got)
	}
	if got := (Axes{Audio: true}).Or(Axes{}); !got.Audio || got.Video {
		t.Errorf("Or with nothing to add = %s, want the audio alone", got)
	}
}
