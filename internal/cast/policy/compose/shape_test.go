package compose

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stupside/castor/internal/media"
)

func TestPassthroughOnlyWhenEverythingChecks(t *testing.T) {
	caps := media.Capabilities{
		SelfFetch: true, Containers: []string{media.HLS},
		Video: []media.VideoSupport{{Codec: media.CodecH264}},
		Audio: []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
	}
	for _, tt := range []struct {
		name       string
		headers    http.Header
		unmeasured bool
		height     int
		maxHeight  media.HeightCap
		want       bool
	}{
		{name: "a header-free measured source at the ceiling passes through", height: 1080, maxHeight: 1080, want: true},
		{name: "an unmeasured program is not assumed decodable", unmeasured: true},
		{name: "a header-gated source is served", headers: http.Header{"Referer": {"https://player.example/"}}},
		{name: "a source above the ceiling is served, since a hand-off cannot be downscaled", height: 1600, maxHeight: 1080},
	} {
		t.Run(tt.name, func(t *testing.T) {
			program, err := media.NewProgram(media.Program{
				Inputs: []media.Input{{
					ID: media.PrimaryInputID, URL: &url.URL{Scheme: "https", Host: "cdn.example", Path: "/media"},
					ContentType: media.HLS, Headers: tt.headers,
				}},
				Tracks: []media.TrackRef{
					{Input: media.PrimaryInputID, Kind: media.TrackVideo, Optional: true},
					{Input: media.PrimaryInputID, Kind: media.TrackAudio, Optional: true},
				},
				ClockInput: media.PrimaryInputID,
				EndPolicy:  media.EndAtLongest,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !tt.unmeasured {
				program.SetMeasurement(media.ProbeInfo{VideoCodec: media.CodecH264, VideoBitDepth: 8, AudioCodec: media.CodecAAC, AudioChannels: 2})
			}
			shape := Shape{Renderer: caps, Program: program, Height: tt.height, MaxHeight: tt.maxHeight}
			if got := shape.Passthrough(); got != tt.want {
				t.Errorf("Passthrough() = %v, want %v (shape: %s)", got, tt.want, shape)
			}
		})
	}
}

func TestAHandedOverProgramIsOneTheRendererPlaysWhole(t *testing.T) {
	r := media.Capabilities{
		Video: []media.VideoSupport{{Codec: media.CodecH264}},
		Audio: []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
	}
	for _, tt := range []struct {
		name string
		info media.ProbeInfo
		want bool
	}{
		{"supported video without audio", media.ProbeInfo{VideoCodec: media.CodecH264, VideoBitDepth: 8}, true},
		{"unknown video is not evidence of compatibility", media.ProbeInfo{AudioCodec: media.CodecAAC, AudioChannels: 2}, false},
		{"unsupported audio", media.ProbeInfo{VideoCodec: media.CodecH264, VideoBitDepth: 8, AudioCodec: media.CodecEAC3, AudioChannels: 6}, false},
		{"HDR is never assumed safe", media.ProbeInfo{VideoCodec: media.CodecH264, VideoBitDepth: 8, VideoHDR: true}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := canPlay(r, tt.info); got != tt.want {
				t.Errorf("canPlay = %v, want %v", got, tt.want)
			}
		})
	}
}
