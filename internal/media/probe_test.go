package media

import (
	"testing"
	"time"
)

// probeJSON builds the document ffprobe writes for the entries ProbeEntries asks for. The
// tests below are written as the JSON an origin really produces rather than as a struct,
// because the decoder's whole job is that mapping and a struct would test nothing.
func probeJSON(format, streams string) []byte {
	return []byte(`{"streams":[` + streams + `],"format":{` + format + `}}`)
}

const (
	vodFormat  = `"format_name":"mov,mp4,m4a,3gp,3g2,mj2","bit_rate":"6200000","duration":"5405.400000"`
	h264Stream = `{"codec_type":"video","codec_name":"h264","profile":"High","width":1920,"height":1080,"pix_fmt":"yuv420p"}`
	aacStream  = `{"codec_type":"audio","codec_name":"aac","channels":2}`
)

// TestDecodeProbeIdentifiesOneVideoTrackForBothLayers is the reason there is one decoder.
//
// Two used to read the same ffprobe: the source layer's required dimensions and a codec that
// decodes to motion, while the cast layer took the first stream calling itself video. So a
// slideshow was refused as a candidate and, cast by hand, was copy-decided as if it carried
// a picture, and the mjpeg-first row below is exactly that disagreement.
func TestDecodeProbeIdentifiesOneVideoTrackForBothLayers(t *testing.T) {
	for _, tc := range []struct {
		name         string
		streams      string
		wantVideo    Codec
		wantHeight   int
		wantAudio    Codec
		wantPlayable bool
	}{{
		name:         "a real program is both of its tracks",
		streams:      h264Stream + "," + aacStream,
		wantVideo:    CodecH264,
		wantHeight:   1080,
		wantAudio:    CodecAAC,
		wantPlayable: true,
	}, {
		// The decoy an aggregator serves: one still image published as a video track, often at
		// the highest bandwidth in the pool. Nothing a renderer plays comes out of it.
		name:      "an image track is not a picture",
		streams:   `{"codec_type":"video","codec_name":"mjpeg","width":640,"height":360},` + aacStream,
		wantAudio: CodecAAC,
	}, {
		// The row the two decoders disagreed on. The real track is not first, so "the first
		// stream calling itself video" answers mjpeg at 360 lines: a copy decision about a
		// codec that is not being read, and a height that is not the picture's.
		name:         "a thumbnail ahead of the real track is skipped, not preferred",
		streams:      `{"codec_type":"video","codec_name":"mjpeg","width":640,"height":360},` + h264Stream + "," + aacStream,
		wantVideo:    CodecH264,
		wantHeight:   1080,
		wantAudio:    CodecAAC,
		wantPlayable: true,
	}, {
		// A track with no dimensions has nothing behind it either, whatever it calls itself.
		name:      "a video track with no dimensions is not a picture",
		streams:   `{"codec_type":"video","codec_name":"h264","width":0,"height":0},` + aacStream,
		wantAudio: CodecAAC,
	}, {
		// The other decoy shape, and the reason Playable needs both halves: it probes cleanly
		// and cannot be remuxed into anything with sound.
		name:       "video with no audio at all is not playable",
		streams:    h264Stream,
		wantVideo:  CodecH264,
		wantHeight: 1080,
	}, {
		// The first audio track is the one a pull maps as 0:a:0; a later commentary track is
		// not what will be read, so its channel count must not decide the audio axis.
		name:         "a later alternate audio track does not replace the default",
		streams:      h264Stream + "," + aacStream + `,{"codec_type":"audio","codec_name":"ac3","channels":6}`,
		wantVideo:    CodecH264,
		wantHeight:   1080,
		wantAudio:    CodecAAC,
		wantPlayable: true,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := DecodeProbe(probeJSON(vodFormat, tc.streams))
			if err != nil {
				t.Fatalf("DecodeProbe: %v", err)
			}
			if info.VideoCodec != tc.wantVideo {
				t.Errorf("VideoCodec = %q, want %q", info.VideoCodec, tc.wantVideo)
			}
			if info.VideoHeight != tc.wantHeight {
				t.Errorf("VideoHeight = %d, want %d", info.VideoHeight, tc.wantHeight)
			}
			if info.AudioCodec != tc.wantAudio {
				t.Errorf("AudioCodec = %q, want %q", info.AudioCodec, tc.wantAudio)
			}
			if info.AudioChannels != 0 && info.AudioChannels != 2 {
				t.Errorf("AudioChannels = %d, want the default track's 2", info.AudioChannels)
			}
			if got := info.Playable(); got != tc.wantPlayable {
				t.Errorf("Playable() = %v, want %v", got, tc.wantPlayable)
			}
		})
	}
}

// TestDecodeProbeReadsTheContainersOwnFacts covers what the source layer ranks and paces on.
// The duration is the sharpest of them: it separates a feature from a spliced-in ad, it is
// what a projected runtime divides, and where no document publishes one it is the only
// witness to whether a source ends at all.
func TestDecodeProbeReadsTheContainersOwnFacts(t *testing.T) {
	for _, tc := range []struct {
		name         string
		format       string
		wantType     string
		wantBitRate  int64
		wantDuration time.Duration
	}{{
		name:         "a VOD file states all three",
		format:       vodFormat,
		wantType:     MP4,
		wantBitRate:  6_200_000,
		wantDuration: 5405400 * time.Millisecond,
	}, {
		// The shape ffprobe reports for most playlists: it read the source perfectly well and
		// has no runtime and no top-level rate to report. Zero is the absence of a
		// measurement, and reading it as an answer is what made every VOD playlist live.
		name:     "a playlist ffprobe can put no numbers on is still a measurement",
		format:   `"format_name":"hls,applehttp","bit_rate":"N/A","duration":"N/A"`,
		wantType: HLS,
	}, {
		// A container castor has no name for is not a failure: the answer only chooses input
		// flags and decides whether a renderer might be handed the URL, and an unnamed one
		// answers both the safe way.
		name:         "an unrecognised container is named as unknown, not refused",
		format:       `"format_name":"nut","duration":"60.0"`,
		wantDuration: time.Minute,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := DecodeProbe(probeJSON(tc.format, h264Stream+","+aacStream))
			if err != nil {
				t.Fatalf("DecodeProbe: %v", err)
			}
			if info.ContentType != tc.wantType {
				t.Errorf("ContentType = %q, want %q", info.ContentType, tc.wantType)
			}
			if info.BitRate != tc.wantBitRate {
				t.Errorf("BitRate = %d, want %d", info.BitRate, tc.wantBitRate)
			}
			if info.Duration != tc.wantDuration {
				t.Errorf("Duration = %s, want %s", info.Duration, tc.wantDuration)
			}
		})
	}
}

// TestDecodeProbeRefusesAnAnswerThatMeasuredNothing pins the one failure the decoder owns.
// ffprobe writes a format name for every input it opened at all, so a document without one
// is not a measurement, and admitting it as an empty one is how a candidate with nothing
// behind it used to rank as measured.
func TestDecodeProbeRefusesAnAnswerThatMeasuredNothing(t *testing.T) {
	if _, err := DecodeProbe(probeJSON(`"bit_rate":"1000"`, h264Stream)); err == nil {
		t.Error("a document with no format name must fail: nothing was measured")
	}
	if _, err := DecodeProbe([]byte("not json")); err == nil {
		t.Error("an unparseable answer must fail")
	}
}

// TestDecodeProbeDerivesTheEnvelopeACopyIsJudgedBy covers the three fields only the cast layer
// reads, and they are the ones whose wrong answer is silent: a copy admitted over a profile or
// a bit depth a renderer cannot decode plays as a black screen with sound.
func TestDecodeProbeDerivesTheEnvelopeACopyIsJudgedBy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stream    string
		wantDepth int
		wantHDR   bool
	}{
		{"8-bit formats carry no depth marker", `"pix_fmt":"yuv420p"`, 8, false},
		{"no pix_fmt at all is unknown rather than 8", `"codec_name":"hevc"`, 0, false},
		{"PQ at 10 bits is the HDR envelope", `"pix_fmt":"yuv420p10le","color_transfer":"smpte2084"`, 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := `{"codec_type":"video","codec_name":"hevc","profile":"Main 10","width":3840,"height":2160,` + tc.stream + `}`
			info, err := DecodeProbe(probeJSON(vodFormat, stream))
			if err != nil {
				t.Fatalf("DecodeProbe: %v", err)
			}
			if info.VideoProfile != "Main 10" {
				t.Errorf("VideoProfile = %q, want the profile a copy is judged against", info.VideoProfile)
			}
			if info.VideoBitDepth != tc.wantDepth || info.VideoHDR != tc.wantHDR {
				t.Errorf("bit depth = %d HDR = %v, want %d and %v", info.VideoBitDepth, info.VideoHDR, tc.wantDepth, tc.wantHDR)
			}
		})
	}
}

// TestHeightCapAdmitsWhatWasNeverEstablished pins the leniency four parties now share, and
// the direction it may fail in. The cap is asked of a rung a master declared, a height the
// ranker measured, a height a leg's own probe measured and a variant's RESOLUTION during
// selection, and every one of those is legitimately absent sometimes.
func TestHeightCapAdmitsWhatWasNeverEstablished(t *testing.T) {
	const ceiling HeightCap = 1080
	for _, tc := range []struct {
		height int
		want   bool
	}{
		// Nothing established: a URL nobody probed, a playlist with no RESOLUTION.
		{0, true},
		{720, true},
		// Inclusive: a source at exactly the ceiling is what the operator asked for, not one
		// line too many.
		{1080, true},
		{1081, false},
		{2160, false},
	} {
		if got := ceiling.Admits(tc.height); got != tc.want {
			t.Errorf("HeightCap(%d).Admits(%d) = %v, want %v", ceiling, tc.height, got, tc.want)
		}
	}
}
