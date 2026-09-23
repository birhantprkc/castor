package probe

import (
	"slices"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

func probeJSON(format, streams string) []byte {
	return []byte(`{"streams":[` + streams + `],"format":{` + format + `}}`)
}

const (
	vodFormat  = `"format_name":"mov,mp4,m4a,3gp,3g2,mj2","bit_rate":"6200000","duration":"5405.400000"`
	h264Stream = `{"codec_type":"video","codec_name":"h264","profile":"High","width":1920,"height":1080,"pix_fmt":"yuv420p"}`
	aacStream  = `{"codec_type":"audio","codec_name":"aac","channels":2}`
	coverArt   = `{"codec_type":"video","codec_name":"mjpeg","width":640,"height":360,"disposition":{"attached_pic":1}}`
)

func TestDecodeProbeSelectsThePictureAndTheDefaultAudio(t *testing.T) {
	for _, tc := range []struct {
		name       string
		streams    string
		wantVideo  media.Codec
		wantHeight int
	}{
		{"a real program is both of its tracks", h264Stream + "," + aacStream, media.CodecH264, 1080},
		{"attached cover art is not the picture", coverArt + "," + aacStream, "", 0},
		{"a thumbnail ahead of the real track is skipped", coverArt + "," + h264Stream + "," + aacStream, media.CodecH264, 1080},
		// A pull maps 0:a:0, so a later commentary track must not decide the audio axis.
		{"a later alternate audio does not replace the default", h264Stream + "," + aacStream + `,{"codec_type":"audio","codec_name":"ac3","channels":6}`, media.CodecH264, 1080},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := decodeProbeTracks(probeJSON(vodFormat, tc.streams), 0, 0)
			if err != nil {
				t.Fatalf("decodeProbeTracks: %v", err)
			}
			if info.VideoCodec != tc.wantVideo || info.VideoHeight != tc.wantHeight {
				t.Errorf("video = %q at %d, want %q at %d", info.VideoCodec, info.VideoHeight, tc.wantVideo, tc.wantHeight)
			}
			if info.AudioCodec != media.CodecAAC || info.AudioChannels != 2 {
				t.Errorf("audio = %q/%d, want the default AAC stereo track", info.AudioCodec, info.AudioChannels)
			}
		})
	}
}

func TestDecodeProbeReadsTheContainersOwnFacts(t *testing.T) {
	for _, tc := range []struct {
		name         string
		format       string
		wantType     string
		wantBitRate  int64
		wantDuration time.Duration
	}{
		{"a VOD file states all three", vodFormat, media.MP4, 6_200_000, 5405400 * time.Millisecond},
		{"a playlist with no numbers is still a measurement", `"format_name":"hls,applehttp","bit_rate":"N/A","duration":"N/A"`, media.HLS, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := decodeProbeTracks(probeJSON(tc.format, h264Stream+","+aacStream), 0, 0)
			if err != nil {
				t.Fatalf("decodeProbeTracks: %v", err)
			}
			if info.ContentType != tc.wantType || info.BitRate != tc.wantBitRate || info.Duration != tc.wantDuration {
				t.Errorf("facts = %q/%d/%s, want %q/%d/%s", info.ContentType, info.BitRate, info.Duration, tc.wantType, tc.wantBitRate, tc.wantDuration)
			}
		})
	}
}

// Every 0:V:N slot is reported, since DASH representation choice indexes into it.
func TestDecodeProbeReportsEveryPictureItSaw(t *testing.T) {
	streams := coverArt + `,{"codec_type":"video","codec_name":"h264","width":1280,"height":720},` +
		`{"codec_type":"video","codec_name":"h264","width":3840,"height":2160},` +
		`{"codec_type":"video","codec_name":"h264","width":0,"height":0},` + aacStream
	info, err := decodeProbeTracks(probeJSON(vodFormat, streams), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(info.VideoHeights, []int{720, 2160, 0}) {
		t.Errorf("VideoHeights = %v, want every slot with the dimensionless one preserved", info.VideoHeights)
	}
	if info.VideoHeight != 2160 {
		t.Errorf("VideoHeight = %d, want the selected slot's 2160", info.VideoHeight)
	}
}
