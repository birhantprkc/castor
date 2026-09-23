package ffmpeg

import (
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/policy/plan"
	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/media"
)

func TestEachInputIsOpenedOnItsOwnTerms(t *testing.T) {
	program := mustProgram(t, media.Program{Inputs: []media.Input{
		{ID: "picture", URL: mustURL(t, "https://video.test/master.m3u8"), Headers: http.Header{"X-Video-Token": {"v"}}, ContentType: media.HLS},
		{ID: "sound", URL: mustURL(t, "https://audio.test/track.m4a"), Headers: http.Header{"X-Audio-Token": {"a"}}, ContentType: media.MP4},
	}, Tracks: []media.TrackRef{
		{Input: "picture", Kind: media.TrackVideo},
		{Input: "sound", Kind: media.TrackAudio, Index: 2},
	}, ClockInput: "picture", EndPolicy: media.EndAtLongest})
	source := mustProgramSource(t, program, read.Plan{
		"picture": {
			Deadline: time.Second, SegmentRetries: 4, Backoff: time.Minute, RetryStatuses: []int{429, 503},
			Pace: read.Pace{Realtime: 2, Burst: 90 * time.Second},
		},
		// A direct file is never handed -seg_max_retry, which its demuxer aborts on.
		"sound": {Deadline: 2 * time.Second, SegmentRetries: 4},
	})
	args := mustPullArgs(t, copyingPull(source))

	for _, want := range [][]string{
		slices.Concat(demuxFlags, []string{
			"-readrate", "2.0", "-readrate_initial_burst", "90",
			"-rw_timeout", "1000000",
			"-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "60", "-reconnect_on_http_error", "429,503",
			"-headers", "X-Video-Token: v\r\n",
			"-allowed_extensions", "ALL", "-allowed_segment_extensions", "ALL",
			"-extension_picky", "0", "-seg_format_options", "extension_picky=0",
			"-seg_max_retry", "4", "-i", "https://video.test/master.m3u8",
		}),
		slices.Concat(demuxFlags, []string{"-rw_timeout", "2000000", "-headers", "X-Audio-Token: a\r\n", "-i", "https://audio.test/track.m4a"}),
		{"-map", "0:V:0", "-map", "1:a:2"},
	} {
		if !containsSequence(args, want) {
			t.Errorf("argv\n%q\nwant the sequence\n%q", args, want)
		}
	}
}

func TestOnlySelectedTracksAreMapped(t *testing.T) {
	program := mustProgram(t, media.Program{
		Inputs:     []media.Input{{ID: "silent", URL: mustURL(t, "https://media.test/silent.mp4"), ContentType: media.MP4}},
		Tracks:     []media.TrackRef{{Input: "silent", Kind: media.TrackVideo, Optional: true}},
		ClockInput: "silent",
		EndPolicy:  media.EndAtLongest,
	})
	args := mustPullArgs(t, copyingPull(mustProgramSource(t, program, nil)))
	var maps []string
	for i, a := range args[:len(args)-1] {
		if a == "-map" {
			maps = append(maps, args[i+1])
		}
	}
	if !slices.Equal(maps, []string{"0:V:0?"}) {
		t.Errorf("maps = %q, want only the optional video", maps)
	}
}

func TestOffsetsAndEndPolicyAreRendered(t *testing.T) {
	program := mustProgram(t, media.Program{Inputs: []media.Input{
		{ID: "video", URL: mustURL(t, "https://media.test/video.mp4"), ContentType: media.MP4},
		{ID: "audio", URL: mustURL(t, "https://media.test/audio.m4a"), ContentType: media.MP4},
	}, Tracks: []media.TrackRef{{Input: "video", Kind: media.TrackVideo}, {Input: "audio", Kind: media.TrackAudio}}, ClockInput: "video",
		Offsets:   map[media.InputID]time.Duration{"audio": 1250 * time.Millisecond},
		EndPolicy: media.EndAtShortest})
	source := mustProgramSource(t, program, nil)
	for name, args := range map[string][]string{
		"pull":   mustPullArgs(t, copyingPull(source)),
		"encode": mustEncodeArgs(t, EncodeOptions{Input: FromSource(source), Format: mp4Format, Video: plan.CopyVideo(), Audio: aacAudio}),
	} {
		if !containsSequence(args, []string{"-itsoffset", "1.25", "-i", "https://media.test/audio.m4a"}) {
			t.Errorf("%s: audio offset is not attached to its input: %q", name, args)
		}
		if !slices.Contains(args, "-shortest") {
			t.Errorf("%s: end-at-shortest did not render -shortest: %q", name, args)
		}
	}
}

func mustProgram(t *testing.T, program media.Program) media.Program {
	t.Helper()
	program, err := media.NewProgram(program)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func mustProgramSource(t *testing.T, program media.Program, policies read.Plan) ProgramSource {
	t.Helper()
	if policies == nil {
		policies = make(read.Plan, len(program.Inputs))
		for _, input := range program.Inputs {
			policies[input.ID] = read.Policy{}
		}
	}
	source, err := NewProgramSource(program, policies)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func muxedSource(t *testing.T, u *url.URL, contentType string, policy read.Policy) ProgramSource {
	t.Helper()
	program := mustProgram(t, media.Program{
		Inputs: []media.Input{{ID: media.PrimaryInputID, URL: u, ContentType: contentType}},
		Tracks: []media.TrackRef{
			{Input: media.PrimaryInputID, Kind: media.TrackVideo, Optional: true},
			{Input: media.PrimaryInputID, Kind: media.TrackAudio, Optional: true},
		},
		ClockInput: media.PrimaryInputID,
		EndPolicy:  media.EndAtLongest,
	})
	return mustProgramSource(t, program, read.Plan{media.PrimaryInputID: policy})
}
