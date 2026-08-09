package media

import "testing"

// h264 returns an in-envelope 1080p H.264 probe result, so tests can vary one
// field at a time.
func h264() ProbeInfo {
	return ProbeInfo{
		VideoCodec:    CodecH264,
		VideoProfile:  "High",
		VideoHeight:   1080,
		VideoBitDepth: 8,
	}
}

// samsungLike mirrors the H.264 copy envelope the device package pairs with a
// renderer that advertises H.264, so the matching rules can be exercised here
// without importing the device package.
var samsungLike = Renderer{
	Containers: []string{MPEGTS, MP4},
	Video: []VideoSupport{
		{Codec: CodecH264, Profiles: []string{"Constrained Baseline", "Baseline", "Main", "High"}},
	},
}

func TestRendererSupportsCodec(t *testing.T) {
	if !samsungLike.SupportsCodec(CodecH264) {
		t.Error("an H.264 renderer should support H.264")
	}
	if samsungLike.SupportsCodec(CodecHEVC) {
		t.Error("an H.264-only renderer must not report HEVC support")
	}
}

func TestRendererCanCopyVideo(t *testing.T) {
	tests := []struct {
		name string
		info ProbeInfo
		want bool
	}{
		{"in-envelope high", h264(), true},
		{"main profile", withProfile(h264(), "Main"), true},
		{"baseline profile", withProfile(h264(), "Baseline"), true},
		{"constrained baseline", withProfile(h264(), "Constrained Baseline"), true},
		{"resolution is not part of the envelope", withHeight(h264(), 2160), true},
		{"high 10 rejected", withProfile(h264(), "High 10"), false},
		{"high 4:2:2 rejected", withProfile(h264(), "High 4:2:2"), false},
		{"unknown profile rejected", withProfile(h264(), ""), false},
		{"10-bit rejected", withBitDepth(h264(), 10), false},
		// The envelope answers what the renderer said it decodes, and an HDR stream in
		// an advertised profile is inside it. Whether castor HANDS one over is a
		// separate question with a separate answer (no, on any leg and for any
		// renderer), and it is asked where the copy is decided rather than here: it
		// used to live in this predicate, which meant the leg that trusts a renderer
		// past its envelope never asked it at all. Its coverage is
		// TestDecideVideo/"an HDR source is never handed over on any policy".
		{"hdr inside an advertised profile is inside the envelope", withHDR(h264()), true},
		{"hevc rejected", withCodec(h264(), CodecHEVC), false},
		{"zero value rejected", ProbeInfo{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := samsungLike.CanCopyVideo(tt.info); got != tt.want {
				t.Errorf("CanCopyVideo = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRendererCanCopyVideoEmptyBitDepths pins BitDepths' nil-and-empty-both-
// default-to-8-bit convention against the asymmetric bug where a non-nil
// empty slice (e.g. from a decoded JSON "[]") silently rejected every probe,
// unlike Profiles, whose "nil or empty = any" convention already covered
// both cases.
func TestRendererCanCopyVideoEmptyBitDepths(t *testing.T) {
	r := Renderer{Video: []VideoSupport{{Codec: CodecH264, BitDepths: []int{}}}}
	if !r.CanCopyVideo(h264()) {
		t.Error("a non-nil empty BitDepths should default to {8}, same as nil, and accept an 8-bit source")
	}
}

func TestRendererAcceptsContainer(t *testing.T) {
	r := Renderer{Containers: []string{HLS, MP4}}
	if !r.AcceptsContainer(HLS) {
		t.Error("HLS is listed; should be accepted")
	}
	if r.AcceptsContainer(MKV) {
		t.Error("MKV is not listed; should not be accepted")
	}
	// A renderer with no video envelope never reports a copy-eligible stream.
	if (Renderer{}).CanCopyVideo(h264()) {
		t.Error("empty renderer should copy nothing")
	}
}

// avReceiver mirrors the audio envelope the device package pairs with a renderer
// that advertises AAC and AC-3: stereo-capped AAC (its multichannel form is a
// separate, rarely-advertised profile) and uncapped AC-3 (inherently surround).
var avReceiver = Renderer{
	Audio: []AudioSupport{
		{Codec: CodecAAC, MaxChannels: 2},
		{Codec: CodecAC3},
	},
}

func TestRendererSupportsAudioCodec(t *testing.T) {
	if !avReceiver.SupportsAudioCodec(CodecAC3) {
		t.Error("an AC-3 renderer should support AC-3")
	}
	if avReceiver.SupportsAudioCodec(CodecEAC3) {
		t.Error("a renderer that advertises no E-AC-3 must not report it")
	}
}

func TestRendererCanCopyAudio(t *testing.T) {
	audio := func(c Codec, ch int) ProbeInfo { return ProbeInfo{AudioCodec: c, AudioChannels: ch} }
	tests := []struct {
		name string
		info ProbeInfo
		want bool
	}{
		{"stereo aac copies", audio(CodecAAC, 2), true},
		{"5.1 aac exceeds the stereo aac ceiling", audio(CodecAAC, 6), false},
		{"unknown-channel aac with matching codec is trusted", audio(CodecAAC, 0), true},
		{"5.1 ac3 copies (surround codec, no ceiling)", audio(CodecAC3, 6), true},
		{"7.1 ac3 copies (surround codec, no ceiling)", audio(CodecAC3, 8), true},
		{"unadvertised codec rejected", audio(CodecEAC3, 6), false},
		{"zero value rejected", ProbeInfo{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := avReceiver.CanCopyAudio(tt.info); got != tt.want {
				t.Errorf("CanCopyAudio = %v, want %v", got, tt.want)
			}
		})
	}
	// A renderer with no audio envelope never reports a copy-eligible track.
	if (Renderer{}).CanCopyAudio(audio(CodecAC3, 6)) {
		t.Error("empty renderer should copy no audio")
	}
}

func withProfile(v ProbeInfo, p string) ProbeInfo { v.VideoProfile = p; return v }
func withHeight(v ProbeInfo, h int) ProbeInfo     { v.VideoHeight = h; return v }
func withBitDepth(v ProbeInfo, d int) ProbeInfo   { v.VideoBitDepth = d; return v }
func withCodec(v ProbeInfo, c Codec) ProbeInfo    { v.VideoCodec = c; return v }
func withHDR(v ProbeInfo) ProbeInfo               { v.VideoHDR = true; return v }
