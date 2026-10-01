package plan

import (
	"context"
	"slices"
	"testing"

	"github.com/stupside/castor/internal/media"
)

func hostEncoders(_ context.Context, codec media.Codec) (Encoder, bool) {
	switch codec {
	case media.CodecHEVC:
		return Encoder{Name: "hevc_videotoolbox", Codec: media.CodecHEVC, Hardware: true}, true
	case media.CodecH264:
		return Encoder{Name: "libx264", Codec: media.CodecH264}, true
	}
	return Encoder{}, false
}

func TestSelectVideoEncoder(t *testing.T) {
	decodes := func(codecs ...media.Codec) media.Capabilities {
		var r media.Capabilities
		for _, c := range codecs {
			r.Video = append(r.Video, media.VideoSupport{Codec: c})
		}
		return r
	}
	hevcHW := Encoder{Name: "hevc_videotoolbox", Codec: media.CodecHEVC, Hardware: true}
	hevcSW := Encoder{Name: "libx265", Codec: media.CodecHEVC}
	h264HW := Encoder{Name: "h264_videotoolbox", Codec: media.CodecH264, Hardware: true}
	h264SW := Encoder{Name: "libx264", Codec: media.CodecH264}

	for _, tt := range []struct {
		name  string
		caps  media.Capabilities
		avail map[media.Codec]Encoder
		want  string
	}{
		{"hardware HEVC first", decodes(media.CodecHEVC, media.CodecH264), map[media.Codec]Encoder{media.CodecHEVC: hevcHW, media.CodecH264: h264HW}, "hevc_videotoolbox"},
		{"software HEVC is never run live", decodes(media.CodecHEVC, media.CodecH264), map[media.Codec]Encoder{media.CodecHEVC: hevcSW, media.CodecH264: h264HW}, "h264_videotoolbox"},
		{"only what the renderer decodes", decodes(media.CodecH264), map[media.Codec]Encoder{media.CodecHEVC: hevcHW, media.CodecH264: h264SW}, "libx264"},
		{"nothing in common", decodes(media.CodecVP8), map[media.Codec]Encoder{media.CodecH264: h264SW}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _, _ := selectVideoEncoder(tt.caps, func(c media.Codec) (Encoder, bool) { e, ok := tt.avail[c]; return e, ok })
			if got.Name != tt.want {
				t.Errorf("selectVideoEncoder = %q, want %q", got.Name, tt.want)
			}
		})
	}
}

func TestWhatForcesAVideoReEncode(t *testing.T) {
	h264 := media.Capabilities{Video: []media.VideoSupport{{Codec: media.CodecH264}}}
	hdrCapable := media.Capabilities{Video: []media.VideoSupport{{Codec: media.CodecHEVC, Profiles: []media.Profile{"Main 10"}, BitDepths: []int{8, 10}}, {Codec: media.CodecH264}}}
	sdr := media.ProbeInfo{VideoCodec: media.CodecH264, VideoHeight: 720, VideoBitDepth: 8}
	tall := media.ProbeInfo{VideoCodec: media.CodecH264, VideoHeight: 2160, VideoBitDepth: 8}
	hdr := media.ProbeInfo{VideoCodec: media.CodecHEVC, VideoProfile: "Main 10", VideoHeight: 720, VideoBitDepth: 10, VideoHDR: true}

	for _, tt := range []struct {
		name   string
		caps   media.Capabilities
		probe  media.ProbeInfo
		burnIn string
		want   []PlanReason
	}{
		{name: "nothing against it copies", caps: h264, probe: sdr},
		{name: "a source above the ceiling", caps: h264, probe: tall, want: []PlanReason{reasonHeightLimit}},
		{name: "HDR even on a renderer that decodes it", caps: hdrCapable, probe: hdr, want: []PlanReason{reasonHDRPolicy}},
		{name: "a burn-in needs decoded frames", caps: h264, probe: sdr, burnIn: "/tmp/cue.txt", want: []PlanReason{reasonSubtitleBurnIn}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := Inputs{Caps: tt.caps, Probe: tt.probe, Into: testFormat(t, media.MPEGTS), MaxHeight: 1080, BurnIn: tt.burnIn, Encoders: hostEncoders}
			track, refused, err := decideVideo(t.Context(), in)
			if err != nil {
				t.Fatalf("decideVideo: %v", err)
			}
			reasons := make([]PlanReason, 0, len(refused))
			for _, r := range refused {
				reasons = append(reasons, r.Reason)
			}
			if !slices.Equal(reasons, tt.want) {
				t.Errorf("reasons = %v, want %v", reasons, tt.want)
			}
			if enc, ok := track.Encode(); ok && (enc.MaxHeight != in.MaxHeight || enc.SubtitleTextFile != tt.burnIn) {
				t.Errorf("re-encode carries ceiling %d and burn-in %q, want %d and %q", enc.MaxHeight, enc.SubtitleTextFile, in.MaxHeight, tt.burnIn)
			}
		})
	}
}
