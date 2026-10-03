package media

import "testing"

func TestDeviceCanCopyVideo(t *testing.T) {
	envelope := Capabilities{Video: []VideoSupport{
		{Codec: CodecH264, Profiles: []Profile{"Constrained Baseline", "Baseline", "Main", "High"}},
	}}
	base := ProbeInfo{VideoCodec: CodecH264, VideoProfile: "High", VideoHeight: 1080, VideoBitDepth: 8}
	for _, tt := range []struct {
		name   string
		mutate func(*ProbeInfo)
		want   bool
	}{
		{"in-envelope high", func(*ProbeInfo) {}, true},
		{"resolution is not part of the envelope", func(p *ProbeInfo) { p.VideoHeight = 2160 }, true},
		{"high 10 rejected", func(p *ProbeInfo) { p.VideoProfile = "High 10" }, false},
		{"unknown profile rejected", func(p *ProbeInfo) { p.VideoProfile = "" }, false},
		{"10-bit rejected", func(p *ProbeInfo) { p.VideoBitDepth = 10 }, false},
		{"hevc rejected", func(p *ProbeInfo) { p.VideoCodec = CodecHEVC }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			info := base
			tt.mutate(&info)
			if got := envelope.CanCopyVideo(info); got != tt.want {
				t.Errorf("CanCopyVideo = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeviceCanCopyAudio(t *testing.T) {
	envelope := Capabilities{Audio: []AudioSupport{{Codec: CodecAAC, MaxChannels: 2}, {Codec: CodecAC3}}}
	for _, tt := range []struct {
		name  string
		codec Codec
		ch    int
		want  bool
	}{
		{"stereo aac copies", CodecAAC, 2, true},
		{"5.1 aac exceeds the stereo aac ceiling", CodecAAC, 6, false},
		{"5.1 ac3 copies", CodecAC3, 6, true},
		{"unadvertised codec rejected", CodecEAC3, 6, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := envelope.CanCopyAudio(ProbeInfo{AudioCodec: tt.codec, AudioChannels: tt.ch}); got != tt.want {
				t.Errorf("CanCopyAudio = %v, want %v", got, tt.want)
			}
		})
	}
}
