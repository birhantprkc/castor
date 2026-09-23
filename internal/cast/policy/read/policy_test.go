package read

import (
	"testing"

	"github.com/stupside/castor/internal/container"
	"github.com/stupside/castor/internal/media"
)

func TestAnEncodesCeilingIsWhatItsOutputCanDrain(t *testing.T) {
	for _, tt := range []struct {
		name               string
		segmented, burning bool
		want               Pace
	}{
		{"a progressive output imposes nothing", false, false, Pace{}},
		{"a segment window is drained at 1x", true, false, Pace{Realtime: 1, Burst: container.HLSWindow}},
		{"a burn-in keeps its own lead even into a window", true, true, paceBurning},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Ceiling(tt.segmented, tt.burning); got != tt.want {
				t.Errorf("Ceiling = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAnUnboundedEncodePacesOnlyItsSegmentedInputs(t *testing.T) {
	plan := Plan{"video": {Pace: paceVOD}, "audio": {Pace: paceVOD}}
	got := plan.Encoding(mixedProgram(t), Ceiling(false, false))
	if got["video"].Pace != paceVOD || got["audio"].Pace != (Pace{}) {
		t.Errorf("playlist paced %+v and progressive input %+v, want %+v and unpaced", got["video"].Pace, got["audio"].Pace, paceVOD)
	}
	if plan["audio"].Pace != paceVOD {
		t.Error("Encoding edited the plan it was handed")
	}
}

func TestASegmentedOutputBoundsTheReadItDoesNotReplaceIt(t *testing.T) {
	for _, tt := range []struct {
		name      string
		own, want Pace
	}{
		{"a live edge keeps its empty burst", paceLive, Pace{Realtime: 1}},
		{"a VOD source is held to one window", paceVOD, Pace{Realtime: 1, Burst: container.HLSWindow}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Plan{"video": {Pace: tt.own}, "audio": {Pace: tt.own}}.Encoding(mixedProgram(t), Ceiling(true, false))
			for id, policy := range got {
				if policy.Pace != tt.want {
					t.Errorf("%s reads at %+v, want %+v", id, policy.Pace, tt.want)
				}
			}
		})
	}
}

func mixedProgram(t *testing.T) media.Program {
	t.Helper()
	program, err := media.NewProgram(media.Program{Inputs: []media.Input{
		{ID: "video", URL: planURL(t, "https://video.test/master.m3u8"), ContentType: media.HLS},
		{ID: "audio", URL: planURL(t, "https://audio.test/track.m4a"), ContentType: media.MP4},
	}, Tracks: []media.TrackRef{
		{Input: "video", Kind: media.TrackVideo},
		{Input: "audio", Kind: media.TrackAudio},
	}, ClockInput: "video", EndPolicy: media.EndAtShortest})
	if err != nil {
		t.Fatal(err)
	}
	return program
}
