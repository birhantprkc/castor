package media

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestProgramValidationRejectsInvalidReferences(t *testing.T) {
	validInput := Input{ID: PrimaryInputID, URL: &url.URL{Scheme: "https", Host: "example.test", Path: "/video.mp4"}}
	validTrack := TrackRef{Input: PrimaryInputID, Kind: TrackVideo}
	validClock, validEnd := PrimaryInputID, EndAtLongest

	cases := []struct {
		name    string
		program Program
		want    string
	}{
		{"no inputs", Program{Tracks: []TrackRef{validTrack}, ClockInput: validClock, EndPolicy: validEnd}, "no inputs"},
		{"empty input ID", Program{Inputs: []Input{{URL: validInput.URL}}, Tracks: []TrackRef{validTrack}, ClockInput: validClock, EndPolicy: validEnd}, "has no ID"},
		{"duplicate input ID", Program{Inputs: []Input{validInput, validInput}, Tracks: []TrackRef{validTrack}, ClockInput: validClock, EndPolicy: validEnd}, "duplicated"},
		{"missing URL", Program{Inputs: []Input{{ID: PrimaryInputID}}, Tracks: []TrackRef{validTrack}, ClockInput: validClock, EndPolicy: validEnd}, "has no URL"},
		{"no tracks", Program{Inputs: []Input{validInput}, ClockInput: validClock, EndPolicy: validEnd}, "selects no tracks"},
		{"unknown track input", Program{Inputs: []Input{validInput}, Tracks: []TrackRef{{Input: "missing", Kind: TrackVideo}}, ClockInput: validClock, EndPolicy: validEnd}, "unknown input"},
		{"invalid track kind", Program{Inputs: []Input{validInput}, Tracks: []TrackRef{{Input: PrimaryInputID, Kind: "data"}}, ClockInput: validClock, EndPolicy: validEnd}, "invalid kind"},
		{"negative track index", Program{Inputs: []Input{validInput}, Tracks: []TrackRef{{Input: PrimaryInputID, Kind: TrackVideo, Index: -1}}, ClockInput: validClock, EndPolicy: validEnd}, "negative"},
		{"duplicate track kind", Program{Inputs: []Input{validInput}, Tracks: []TrackRef{validTrack, validTrack}, ClockInput: validClock, EndPolicy: validEnd}, "more than one video"},
		{"unknown clock", Program{Inputs: []Input{validInput}, Tracks: []TrackRef{validTrack}, ClockInput: "missing", EndPolicy: EndAtLongest}, "clock references unknown"},
		{"invalid end policy", Program{Inputs: []Input{validInput}, Tracks: []TrackRef{validTrack}, ClockInput: PrimaryInputID, EndPolicy: "eventually"}, "invalid end policy"},
		{"unknown offset input", Program{Inputs: []Input{validInput}, Tracks: []TrackRef{validTrack}, ClockInput: PrimaryInputID, EndPolicy: EndAtLongest, Offsets: map[InputID]time.Duration{"missing": 0}}, "offset references unknown"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.program.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
	valid := Program{Inputs: []Input{validInput}, Tracks: []TrackRef{validTrack}, ClockInput: validClock, EndPolicy: validEnd}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() = %v on a well-formed program", err)
	}
}
