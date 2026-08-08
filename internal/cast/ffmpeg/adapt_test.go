package ffmpeg

import (
	"slices"
	"testing"

	"github.com/stupside/castor/internal/media"
)

// audioProbe builds the mapped-pair measurement an adaptation reads. Video is filled
// in by default because castor's real pipeline always maps a video track when the
// source has one, and exactly one adaptation (TrueHD into fragmented mp4) changes
// its answer when it does not.
func audioProbe(codec media.Codec) media.ProbeInfo {
	return media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: codec}
}

func videoProbe(codec media.Codec) media.ProbeInfo {
	return media.ProbeInfo{VideoCodec: codec, AudioCodec: media.CodecAAC}
}

// TestCopyMatrix is the pin for what a copy needs in order to WORK. One row per
// cell: every codec castor has a measurement for against every container it
// produces. Each row states the exact contribution, so changing a predicate fails
// here with the cell that moved rather than somewhere downstream.
//
// Whether a copy can work at all is not asserted here. That belongs to the
// carriage package and is pinned by its own matrix; keeping the two apart is the
// point of them being two packages.
func TestCopyMatrix(t *testing.T) {
	type want struct {
		filters    []string
		movFlags   []string
		outputArgs []string
	}
	audio := []struct {
		codec  media.Codec
		format media.FormatInfo
		want   want
	}{
		// AAC: ADTS inside MPEG-TS, AudioSpecificConfig inside MP4. The repack goes
		// toward the out-of-band destinations only; toward mpegts it is exit-0
		// corruption (8 of 189 packets survive).
		{media.CodecAAC, mpegtsFormat, want{}},
		{media.CodecAAC, mp4Format, want{filters: []string{"aac_adtstoasc"}}},
		{media.CodecAAC, hlsFormat, want{filters: []string{"aac_adtstoasc"}}},

		// aac_latm is a different codec id: aac_adtstoasc refuses it at init and no
		// LATM to ASC filter exists, so the mp4 family cannot carry it at all.
		{media.CodecAACLATM, mpegtsFormat, want{}},

		// Dolby derives its sample description box from the first frame, so the mp4
		// muxer must hold its moov back. The hls muxer already does, by construction.
		{media.CodecAC3, mpegtsFormat, want{}},
		{media.CodecAC3, mp4Format, want{movFlags: []string{"delay_moov"}, outputArgs: []string{"-frag_duration", "1000000"}}},
		{media.CodecAC3, hlsFormat, want{}},
		{media.CodecEAC3, mpegtsFormat, want{}},
		{media.CodecEAC3, mp4Format, want{movFlags: []string{"delay_moov"}, outputArgs: []string{"-frag_duration", "1000000"}}},
		{media.CodecEAC3, hlsFormat, want{}},
		{media.CodecTrueHD, mpegtsFormat, want{}},
		{media.CodecTrueHD, mp4Format, want{movFlags: []string{"delay_moov"}, outputArgs: []string{"-frag_duration", "1000000"}}},
		{media.CodecTrueHD, hlsFormat, want{}},

		// These need nothing anywhere: they pass through all three intact, and
		// opus and dts are properly registered in MPEG-TS rather than private data.
		{media.CodecMP3, mpegtsFormat, want{}},
		{media.CodecMP3, mp4Format, want{}},
		{media.CodecMP3, hlsFormat, want{}},
		{media.CodecMP2, mpegtsFormat, want{}},
		{media.CodecMP2, mp4Format, want{}},
		{media.CodecMP2, hlsFormat, want{}},
		{media.CodecOpus, mpegtsFormat, want{}},
		{media.CodecOpus, mp4Format, want{}},
		{media.CodecOpus, hlsFormat, want{}},
		{media.CodecDTS, mpegtsFormat, want{}},
		{media.CodecDTS, mp4Format, want{}},
		{media.CodecDTS, hlsFormat, want{}},

		// MPEG-TS has no stream type for these: exit 0, a plausible "audio:102KiB"
		// muxed, and an output with zero audio streams. Fragmented mp4 carries them.
		{media.CodecFLAC, mp4Format, want{}},
		{media.CodecFLAC, hlsFormat, want{}},
		{media.CodecVorbis, mp4Format, want{}},
		{media.CodecVorbis, hlsFormat, want{}},
		{media.Codec("pcm_s16le"), mp4Format, want{}},
		{media.Codec("pcm_s16le"), hlsFormat, want{}},

		// pcm_mulaw is blocked in BOTH directions, which is why copyability cannot be
		// reduced to a per-codec or per-destination ranking.

		{media.CodecWMAv2, mpegtsFormat, want{}},
	}

	for _, tt := range audio {
		t.Run("audio/"+string(tt.codec)+"/"+tt.format.Muxer, func(t *testing.T) {
			assertPlan(t, planAudioCopy(audioProbe(tt.codec), tt.format),
				tt.want.filters, tt.want.movFlags, tt.want.outputArgs)
		})
	}

	video := []struct {
		codec  media.Codec
		format media.FormatInfo
		want   want
	}{
		// No framing repack in either direction: ffmpeg auto-inserts *_mp4toannexb
		// toward mpegts, and movenc converts Annex B to length-prefixed natively.
		{media.CodecH264, mpegtsFormat, want{}},
		{media.CodecH264, mp4Format, want{}},
		{media.CodecH264, hlsFormat, want{}},

		// hevc copies come out tagged hev1 everywhere; fMP4 players conventionally
		// want hvc1. Fatal on the wrong codec (h264 + -tag:v hvc1 exits 183), hence
		// the codec gate, and excluded from mpegts where it would be inert.
		{media.CodecHEVC, mpegtsFormat, want{}},
		{media.CodecHEVC, mp4Format, want{outputArgs: []string{"-tag:v", "hvc1"}}},
		{media.CodecHEVC, hlsFormat, want{outputArgs: []string{"-tag:v", "hvc1"}}},

		{media.CodecMPEG2, mpegtsFormat, want{}},
		{media.CodecMPEG2, mp4Format, want{}},
		{media.CodecMPEG2, hlsFormat, want{}},
		{media.CodecMPEG4, mpegtsFormat, want{}},
		{media.CodecMPEG4, mp4Format, want{}},
		{media.CodecMPEG4, hlsFormat, want{}},

		// vp8 is the only codec broken in both directions; vp9/av1/mjpeg copy fine
		// into the mp4 family and are destroyed by mpegts.
		{media.CodecVP9, mp4Format, want{}},
		{media.CodecVP9, hlsFormat, want{}},
		{media.CodecAV1, mp4Format, want{}},
		{media.CodecAV1, hlsFormat, want{}},
		{media.CodecMJPEG, mp4Format, want{}},
		{media.CodecMJPEG, hlsFormat, want{}},
	}

	for _, tt := range video {
		t.Run("video/"+string(tt.codec)+"/"+tt.format.Muxer, func(t *testing.T) {
			assertPlan(t, planVideoCopy(videoProbe(tt.codec), tt.format),
				tt.want.filters, tt.want.movFlags, tt.want.outputArgs)
		})
	}
}

func assertPlan(t *testing.T, plan copyPlan, filters, movFlags, outputArgs []string) {
	t.Helper()
	if !slices.Equal(plan.Filters, filters) {
		t.Errorf("Filters = %v, want %v", plan.Filters, filters)
	}
	if !slices.Equal(plan.MovFlags, movFlags) {
		t.Errorf("MovFlags = %v, want %v", plan.MovFlags, movFlags)
	}
	if !slices.Equal(plan.OutputArgs, outputArgs) {
		t.Errorf("OutputArgs = %v, want %v", plan.OutputArgs, outputArgs)
	}
}

// TestNoRepackTowardAnInBandContainer is stated separately from the matrix
// because it is the silent-corruption direction and deserves to fail on its own.
// aac_adtstoasc handed to the mpegts muxer with an ADTS input exits 0, prints
// "AAC bitstream not in ADTS format and extradata missing" 188 times, leaves 8 of
// 189 packets, and produces audio nothing can decode. Nothing about that is
// visible in an exit status, so the only defence is that the filter is never
// emitted toward an in-band destination in the first place.
func TestNoRepackTowardAnInBandContainer(t *testing.T) {
	codecs := []media.Codec{
		media.CodecAAC, media.CodecAACLATM, media.CodecAC3, media.CodecEAC3,
		media.CodecMP3, media.CodecMP2, media.CodecOpus, media.CodecDTS,
		media.CodecTrueHD, media.CodecFLAC, media.CodecVorbis, media.CodecWMAv2,
		media.Codec("pcm_s16le"), media.Codec("pcm_mulaw"),
	}
	for _, c := range codecs {
		plan := planAudioCopy(audioProbe(c), mpegtsFormat)
		if len(plan.Filters) > 0 {
			t.Errorf("%s into %s contributed %v; a repack toward an in-band container exits 0 and destroys the audio",
				c, mpegtsFormat.ContentType, plan.Filters)
		}
	}
	for _, c := range []media.Codec{media.CodecH264, media.CodecHEVC, media.CodecMPEG2, media.CodecMPEG4} {
		plan := planVideoCopy(videoProbe(c), mpegtsFormat)
		if len(plan.Filters) > 0 {
			t.Errorf("%s into %s contributed %v; ffmpeg inserts the right *_mp4toannexb itself toward mpegts",
				c, mpegtsFormat.ContentType, plan.Filters)
		}
	}
}

// TestRepackTowardAnOutOfBandContainer is the other direction, and the one that
// fails loudly: an ADTS-framed AAC track copied into the mp4 muxer without
// aac_adtstoasc exits 255 with "Malformed AAC bitstream detected" and 0 of 189
// audio packets written, and does the same inside the hls muxer's fMP4 segments.
func TestRepackTowardAnOutOfBandContainer(t *testing.T) {
	for _, f := range []media.FormatInfo{mp4Format, hlsFormat} {
		plan := planAudioCopy(audioProbe(media.CodecAAC), f)
		if !slices.Equal(plan.Filters, []string{"aac_adtstoasc"}) {
			t.Errorf("aac into %s contributed %v, want [aac_adtstoasc]; without it the muxer rejects the first audio packet and the encode dies",
				f.ContentType, plan.Filters)
		}
	}
}

// TestUnknownFramingMatchesNoAdaptation covers a FormatInfo built anywhere but
// the registry. Its zero Framing must match nothing rather than defaulting into
// the in-band answer and getting an aac_adtstoasc it cannot survive.
func TestUnknownFramingMatchesNoAdaptation(t *testing.T) {
	undeclared := media.FormatInfo{ContentType: media.MP4, Muxer: media.MuxerMP4}
	if got := planAudioCopy(audioProbe(media.CodecAAC), undeclared).Filters; got != nil {
		t.Errorf("audio filters = %v, want none: an undeclared framing must match no framing-keyed adaptation", got)
	}
	if got := planVideoCopy(videoProbe(media.CodecHEVC), undeclared).OutputArgs; got != nil {
		t.Errorf("video output args = %v, want none", got)
	}
}

// TestDelayMoovIsNotUnconditional pins the cost side of the Dolby fix. delay_moov
// withholds every byte, ftyp included, until the first packet of every mapped
// track has arrived AND the first fragment is cut, which is seconds of a dead
// socket on a 6 s GOP where plain empty_moov had 1239 bytes out at t=0.050 s, and
// 9.56 s when the audio track's first packet lands at t=8 s. So it may only
// appear where the alternative is exit 234 and no cast at all.
func TestDelayMoovIsNotUnconditional(t *testing.T) {
	needs := []media.Codec{media.CodecAC3, media.CodecEAC3, media.CodecTrueHD}
	for _, c := range needs {
		plan := planAudioCopy(audioProbe(c), mp4Format)
		if !slices.Contains(plan.MovFlags, "delay_moov") {
			t.Errorf("%s into mp4 = %v, want delay_moov (\"Cannot write moov atom before AC3 packets\", exit 234)", c, plan.MovFlags)
		}
		if !slices.Contains(plan.OutputArgs, "-frag_duration") {
			t.Errorf("%s into mp4 = %v, want -frag_duration bounding delay_moov's silence", c, plan.OutputArgs)
		}
		// The hls muxer writes init.mp4 after the first packet by construction and
		// copies AC-3 intact with no extra flag.
		if plan := planAudioCopy(audioProbe(c), hlsFormat); len(plan.MovFlags) > 0 || len(plan.OutputArgs) > 0 {
			t.Errorf("%s into hls contributed %v/%v; the hls muxer delays its own init segment", c, plan.MovFlags, plan.OutputArgs)
		}
		if plan := planAudioCopy(audioProbe(c), mpegtsFormat); len(plan.MovFlags) > 0 || len(plan.OutputArgs) > 0 {
			t.Errorf("%s into mpegts contributed %v/%v; mpegts has no moov", c, plan.MovFlags, plan.OutputArgs)
		}
	}
	for _, c := range []media.Codec{media.CodecAAC, media.CodecMP3, media.CodecOpus, media.CodecDTS, media.CodecFLAC} {
		plan := planAudioCopy(audioProbe(c), mp4Format)
		if len(plan.MovFlags) > 0 || len(plan.OutputArgs) > 0 {
			t.Errorf("%s into mp4 contributed %v/%v; it mux identically either way, and delay_moov costs a dead socket",
				c, plan.MovFlags, plan.OutputArgs)
		}
	}
}

// TestNoTrackMatchesNoAdaptation covers a probe that found nothing (a source with
// no audio, or a probe that failed): the optional map will not select a track, so
// there is nothing to adapt and nothing may be emitted.
func TestNoTrackMatchesNoAdaptation(t *testing.T) {
	for _, f := range []media.FormatInfo{mpegtsFormat, mp4Format, hlsFormat} {
		if plan := planAudioCopy(media.ProbeInfo{VideoCodec: media.CodecH264}, f); plan.Filters != nil {
			t.Errorf("a source with no audio track produced %+v for %s", plan, f.ContentType)
		}
		if plan := planVideoCopy(media.ProbeInfo{AudioCodec: media.CodecAAC}, f); plan.OutputArgs != nil {
			t.Errorf("a source with no video track produced %+v for %s", plan, f.ContentType)
		}
	}
}

// TestEveryFormatMuxerHasTuning is the cross-package half of the registry
// coupling: adding a producible format is one row in media.formatRegistry plus one
// containerTuning entry, and EncodeArgs refuses a muxer with no entry, so a
// half-added format must fail here rather than at cast time.
func TestEveryFormatMuxerHasTuning(t *testing.T) {
	for f := range media.ProducibleFormats() {
		if _, ok := containerTuning[f.Muxer]; !ok {
			t.Errorf("format %q declares muxer %q, which has no containerTuning entry", f.ContentType, f.Muxer)
		}
	}
}

// TestHLSSegmentTypeMatchesDeclaredFraming binds the one fact stored in two
// places. The HLS registry row says FramingOutOfBand only because this tuning
// writes fMP4 segments: the same source and the same muxer with
// -hls_segment_type mpegts copies ADTS AAC untouched at exit 0, so declaring
// out-of-band while writing mpegts segments would hand the repack to an in-band
// destination, which exits 0 and leaves 8 of 131 audio packets.
func TestHLSSegmentTypeMatchesDeclaredFraming(t *testing.T) {
	segmentType := argValue(containerTuning[media.MuxerHLS].Args, "-hls_segment_type")
	switch hlsFormat.Framing {
	case media.FramingOutOfBand:
		if segmentType != "fmp4" {
			t.Errorf("the HLS format declares FramingOutOfBand but the muxer writes %q segments", segmentType)
		}
	case media.FramingInBand:
		if segmentType != "mpegts" {
			t.Errorf("the HLS format declares FramingInBand but the muxer writes %q segments", segmentType)
		}
	default:
		t.Fatalf("the HLS format declares no framing")
	}
}
