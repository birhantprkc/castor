package core

import (
	"testing"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/media"
)

// TestPreferredAudioCodecsHaveTargets guards the coupling between the surround
// codec ladder and the bitrate map: a preferred codec with no target would
// silently downmix a source it should have kept in surround.
func TestPreferredAudioCodecsHaveTargets(t *testing.T) {
	for _, c := range audioCodecPreference {
		if _, ok := audioTargets[c]; !ok {
			t.Errorf("codec %q is in audioCodecPreference but has no audioTargets entry", c)
		}
	}
}

func TestDecideAudio(t *testing.T) {
	aacStereo := media.AudioSupport{Codec: media.CodecAAC, MaxChannels: 2}
	ac3 := media.AudioSupport{Codec: media.CodecAC3}
	eac3 := media.AudioSupport{Codec: media.CodecEAC3}
	caps := func(a ...media.AudioSupport) media.Renderer { return media.Renderer{Audio: a} }
	src := func(codec media.Codec, ch int) media.ProbeInfo {
		return media.ProbeInfo{AudioCodec: codec, AudioChannels: ch}
	}

	// Both destinations are resolved from the registry, so a row never describes a
	// container the production path could not have produced.
	mp4Format := testFormat(t, media.MP4)
	mpegtsFormat := testFormat(t, media.MPEGTS)

	tests := []struct {
		name         string
		caps         media.Renderer
		src          media.ProbeInfo
		format       media.FormatInfo
		wantCodec    string
		wantChannels int
	}{
		{"stereo aac is copied", caps(aacStereo, ac3), src(media.CodecAAC, 2), mp4Format, "copy", 0},
		{"5.1 ac3 is copied intact", caps(aacStereo, ac3), src(media.CodecAC3, 6), mp4Format, "copy", 0},
		{"5.1 aac re-encodes to ac3, layout kept", caps(aacStereo, ac3), src(media.CodecAAC, 6), mp4Format, "ac3", 6},
		{"5.1 dts prefers eac3 when advertised", caps(eac3, ac3), src("dts", 6), mp4Format, "eac3", 6},
		{"7.1 folds to the ac3 channel ceiling", caps(ac3), src("dts", 8), mp4Format, "ac3", 6},
		{"7.1 folds to the eac3 5.1 ceiling (ffmpeg eac3 has no 7.1)", caps(eac3), src("dts", 8), mp4Format, "eac3", 6},
		{"multichannel with no surround support downmixes to stereo aac", caps(aacStereo), src("dts", 6), mp4Format, "aac", 2},
		{"stereo source the renderer can't copy downmixes to aac", caps(), src("mp3", 2), mp4Format, "aac", 2},
		{"conservative caps fall to the stereo aac floor", media.Renderer{}, media.ProbeInfo{}, mp4Format, "aac", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			track := DecideAudio(t.Context(), AudioInputs{Caps: tt.caps, Probe: tt.src, Into: tt.format})
			if got := track.Name(); got != tt.wantCodec {
				t.Errorf("audio codec = %q, want %q", got, tt.wantCodec)
			}
			enc, reencoded := track.Encode()
			if reencoded != (tt.wantCodec != "copy") {
				t.Fatalf("re-encoded = %t, want %t", reencoded, tt.wantCodec != "copy")
			}
			// A copy carries no re-encode parameter because it has nowhere to carry
			// one: the fields below only exist inside an AudioEncode.
			if !reencoded {
				return
			}
			if enc.Channels != tt.wantChannels {
				t.Errorf("audio channels = %d, want %d", enc.Channels, tt.wantChannels)
			}
			if enc.Bitrate == "" {
				t.Error("a re-encode must set an audio bitrate")
			}
		})
	}

	// The container question, which is separate from the renderer question and is
	// the difference between adapting and dying. In both rows the renderer decodes
	// the source codec, so the old rule would have copied; the muxer would then
	// have either exited 0 with an output holding no audio stream at all (FLAC into
	// MPEG-TS, written as private data) or exited 234 before a byte (aac_latm into
	// the mp4 family, "Could not find tag for codec aac_latm in stream #0"). The
	// source is never refused for it, only re-encoded.
	uncarriable := []struct {
		name   string
		caps   media.Renderer
		src    media.ProbeInfo
		format media.FormatInfo
	}{
		{"flac the renderer decodes but MPEG-TS cannot carry", caps(media.AudioSupport{Codec: media.CodecFLAC}), src(media.CodecFLAC, 2), mpegtsFormat},
		{"aac_latm the renderer decodes but the mp4 family has no tag for", caps(media.AudioSupport{Codec: media.CodecAACLATM}), src(media.CodecAACLATM, 2), mp4Format},
	}
	for _, tt := range uncarriable {
		t.Run(tt.name, func(t *testing.T) {
			track := DecideAudio(t.Context(), AudioInputs{Caps: tt.caps, Probe: tt.src, Into: tt.format})
			enc, ok := track.Encode()
			if !ok {
				t.Fatalf("audio codec = copy; the output container cannot carry %q", tt.src.AudioCodec)
			}
			if enc.Bitrate == "" {
				t.Error("the fall-through must be a real re-encode, with a bitrate")
			}
		})
	}

	// The converse, so the check reads as per-destination and never as a codec ban:
	// the same FLAC the MPEG-TS row rejects copies into fragmented mp4, which was
	// carries it intact.
	t.Run("flac copies into a container that does carry it", func(t *testing.T) {
		track := DecideAudio(t.Context(), AudioInputs{
			Caps:  caps(media.AudioSupport{Codec: media.CodecFLAC}),
			Probe: src(media.CodecFLAC, 2),
			Into:  mp4Format,
		})
		if enc, ok := track.Encode(); ok {
			t.Errorf("audio codec = %q, want copy: fragmented mp4 carries FLAC", enc.Codec)
		}
	})

	// A track a reader of this cast already died copying is not copied again, whatever the
	// renderer decodes and whatever the container carries. It is the only piece of evidence
	// here that is about the PACKETS rather than about the codec: an ADTS AAC track that
	// arrives truncated is the same codec as one that arrives whole.
	t.Run("an axis a previous attempt's copy broke on is never copied again", func(t *testing.T) {
		in := AudioInputs{
			Caps:   caps(aacStereo),
			Probe:  src(media.CodecAAC, 2),
			Into:   mp4Format,
			Decode: carriage.Axes{Audio: true},
		}
		enc, ok := DecideAudio(t.Context(), in).Encode()
		if !ok {
			t.Fatal("an audio track the reader already died copying was copied again")
		}
		if enc.Bitrate == "" {
			t.Error("the fall-through must be a real re-encode, with a bitrate")
		}
		// Per axis: a blamed VIDEO track is no reason to touch the audio.
		in.Decode = carriage.Axes{Video: true}
		if enc, ok := DecideAudio(t.Context(), in).Encode(); ok {
			t.Errorf("a blamed video axis re-encoded the audio to %q as well", enc.Codec)
		}
	})
}

// testFormat resolves a producible container from the registry, so tests never
// describe a format the pipeline could not have produced (and never hand the
// resolvers a zero FormatInfo, whose unknown framing matches no adaptation).
func testFormat(t *testing.T, contentType string) media.FormatInfo {
	t.Helper()
	f, ok := media.FormatForContentType(contentType)
	if !ok {
		t.Fatalf("no producible format for %q", contentType)
	}
	return f
}
