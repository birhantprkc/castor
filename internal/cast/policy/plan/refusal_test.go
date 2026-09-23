package plan

import (
	"iter"
	"slices"
	"testing"

	"github.com/stupside/castor/internal/media"
)

// planInputs walks the cross product of every fact a plan is decided against.
func planInputs(t *testing.T) iter.Seq[Inputs] {
	mpegts, mp4, hls := testFormat(t, media.MPEGTS), testFormat(t, media.MP4), testFormat(t, media.HLS)
	facts := [][]func(*Inputs){{
		func(in *Inputs) {
			in.Caps = media.Capabilities{
				Video: []media.VideoSupport{{Codec: media.CodecH264}},
				Audio: []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
			}
		},
		func(in *Inputs) {
			in.Caps = media.Capabilities{
				Video: []media.VideoSupport{{Codec: media.CodecHEVC, Profiles: []media.Profile{"Main 10"}, BitDepths: []int{8, 10}}},
				Audio: []media.AudioSupport{{Codec: media.CodecEAC3}},
			}
		},
		func(in *Inputs) {
			in.Caps = media.Capabilities{
				Video: []media.VideoSupport{{Codec: media.CodecVP9}, {Codec: media.CodecH264}},
				Audio: []media.AudioSupport{{Codec: media.CodecFLAC}, {Codec: media.CodecAAC, MaxChannels: 2}},
			}
		},
		// No codec the host encodes, so the video axis fails.
		func(in *Inputs) {
			in.Caps = media.Capabilities{
				Video: []media.VideoSupport{{Codec: media.CodecVP8}},
				Audio: []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
			}
		},
	}, {
		func(in *Inputs) { in.Probe = media.ProbeInfo{} },
		func(in *Inputs) {
			in.Probe = media.ProbeInfo{VideoCodec: media.CodecH264, VideoHeight: 720, VideoBitDepth: 8, AudioCodec: media.CodecAAC, AudioChannels: 2}
		},
		func(in *Inputs) {
			in.Probe = media.ProbeInfo{
				VideoCodec: media.CodecHEVC, VideoProfile: "Main 10", VideoHeight: 2160, VideoBitDepth: 10, VideoHDR: true,
				AudioCodec: media.CodecEAC3, AudioChannels: 6,
			}
		},
		func(in *Inputs) {
			in.Probe = media.ProbeInfo{VideoCodec: media.CodecVP9, VideoHeight: 1080, VideoBitDepth: 8, AudioCodec: media.CodecFLAC, AudioChannels: 2}
		},
		func(in *Inputs) {
			in.Probe = media.ProbeInfo{VideoCodec: media.CodecH264, VideoHeight: 2160, VideoBitDepth: 8, AudioCodec: media.CodecMP3, AudioChannels: 2}
		},
	}, {
		func(in *Inputs) { in.Into = mpegts },
		func(in *Inputs) { in.Into = mp4 },
		func(in *Inputs) { in.Into = hls },
	}, {
		func(in *Inputs) { in.Decode = media.Axes{} },
		func(in *Inputs) { in.Decode = media.Axes{Video: true} },
		func(in *Inputs) { in.Decode = media.Axes{Audio: true} },
	}, {
		func(in *Inputs) { in.MaxHeight = 480 },
		func(in *Inputs) { in.MaxHeight = 2160 },
	}, {
		func(in *Inputs) { in.BurnIn = "" },
		func(in *Inputs) { in.BurnIn = "/tmp/cue.txt" },
	}, {
		func(in *Inputs) { in.Measured = false },
		func(in *Inputs) { in.Measured = true },
	}}
	return func(yield func(Inputs) bool) {
		at := make([]int, len(facts))
		for {
			in := Inputs{Encoders: hostEncoders}
			for i, values := range facts {
				values[at[i]](&in)
			}
			if !yield(in) {
				return
			}
			i := len(at) - 1
			for ; i >= 0; i-- {
				if at[i]++; at[i] < len(facts[i]) {
					break
				}
				at[i] = 0
			}
			if i < 0 {
				return
			}
		}
	}
}

// A copy is exactly the absence of a refusal, and a refused plan always says why.
func TestACopyIsExactlyTheAbsenceOfARefusal(t *testing.T) {
	carriage := slices.Concat(videoCarriageRefusals, audioCarriageRefusals)
	for in := range planInputs(t) {
		video, videoReasons, err := decideVideo(t.Context(), in)
		if err != nil {
			if len(videoReasons) == 0 {
				t.Fatalf("video refused %+v with no reason: %v", in, err)
			}
		} else if _, encoded := video.Encode(); !video.Decided() || encoded == (len(videoReasons) == 0) {
			t.Fatalf("video encoded=%v with %d reasons for %+v", encoded, len(videoReasons), in)
		}

		audio, audioReasons, err := decideAudio(in)
		if err != nil {
			if len(audioReasons) == 0 {
				t.Fatalf("audio refused %+v with no reason: %v", in, err)
			}
		} else if _, encoded := audio.Encode(); !audio.Decided() || encoded == (len(audioReasons) == 0) {
			t.Fatalf("audio encoded=%v with %d reasons for %+v", encoded, len(audioReasons), in)
		}

		// The floor runs before any renderer connects, so none of its reasons may be about one.
		floor, err := Floor(t.Context(), in)
		if err != nil {
			t.Fatalf("the floor refused %+v: %v", in, err)
		}
		if !floor.Video.Decided() || !floor.Audio.Decided() || floor.Encoded().Any() == (len(floor.Reasons()) == 0) {
			t.Fatalf("the floor encodes %s with reasons %v for %+v", floor.Encoded(), floor.Reasons(), in)
		}
		for _, reason := range floor.Reasons() {
			if !slices.ContainsFunc(carriage, func(r refusal) bool { return r.reason == reason }) {
				t.Fatalf("the floor refused %+v for the renderer reason %q", in, reason)
			}
		}
	}
}
