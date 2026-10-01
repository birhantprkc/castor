package transcode

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stupside/castor/internal/cast/fetch"
	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

// ProgramSource binds execution-only read policies to inputs without leaking into media's source model.
type ProgramSource struct {
	program media.Program
	plan    fetch.Plan
	binary  ffmpeg.Binary
}

// NewProgramSource snapshots a program and its complete read plan.
func NewProgramSource(program media.Program, plan fetch.Plan, binary ffmpeg.Binary) (ProgramSource, error) {
	_, video := program.Track(media.TrackVideo)
	_, audio := program.Track(media.TrackAudio)
	if !video && !audio {
		return ProgramSource{}, fmt.Errorf("FFmpeg program source selects neither video nor audio")
	}
	if err := plan.Validate(program); err != nil {
		return ProgramSource{}, fmt.Errorf("FFmpeg program source: %w", err)
	}
	// A representation is castor's to translate; ffmpeg reading the whole manifest would play something else.
	for _, input := range program.Inputs {
		if input.Representation != "" {
			return ProgramSource{}, fmt.Errorf("the %s input reads representation %q, which castor never republished for ffmpeg", input.ID, input.Representation)
		}
	}

	return ProgramSource{program: program.Clone(), plan: maps.Clone(plan), binary: binary}, nil
}

func (s ProgramSource) inputs() []sourceInput {
	inputs := make([]sourceInput, 0, len(s.program.Inputs))
	for _, input := range s.program.Inputs {
		inputs = append(inputs, sourceInput{
			url:         input.URL,
			headers:     input.Headers,
			contentType: input.ContentType,
			fetch:       s.plan[input.ID],
			offset:      s.program.Offsets[input.ID],
		})
	}
	return inputs
}

func (s ProgramSource) outputArgs() []string {
	if s.program.EndPolicy == media.EndAtShortest {
		return []string{"-shortest"}
	}
	return nil
}

func (s ProgramSource) track(kind media.TrackKind) (sourceTrack, bool) {
	ref, ok := s.program.Track(kind)
	if !ok {
		return sourceTrack{}, false
	}
	for index, input := range s.program.Inputs {
		if input.ID == ref.Input {
			return sourceTrack{input: index, index: ref.Index, optional: ref.Optional}, true
		}
	}
	// NewProgramSource validated this binding; unreachable without a programming error inside ProgramSource.
	return sourceTrack{}, false
}

func (s ProgramSource) ProbeInputs() []ffmpeg.ProbeInput {
	inputs := s.inputs()
	out := make([]ffmpeg.ProbeInput, 0, len(inputs))
	for i, input := range inputs {
		args := readArgs(input.fetch)
		args = append(args, ffmpeg.HeaderArgs(input.headers)...)
		args = append(args, ffmpeg.AdaptiveInputArgs(input.contentType, input.fetch.SegmentRetries)...)
		out = append(out, ffmpeg.ProbeInput{ID: s.program.Inputs[i].ID, URL: input.url.String(), Args: args})
	}
	return out
}

type sourceInput struct {
	url         *url.URL
	headers     http.Header
	contentType string
	fetch       fetch.Policy
	offset      time.Duration
}

type sourceTrack struct {
	input    int
	index    int
	optional bool
}

// paceArgs renders read pace as ffmpeg input flags (nil if unpaced).
func paceArgs(p fetch.Pace, binary ffmpeg.Binary) []string {
	if p.Realtime <= 0 {
		return nil
	}
	args := []string{
		"-readrate", formatRate(p.Realtime),
		"-readrate_initial_burst", strconv.Itoa(int(p.Burst.Seconds())),
	}
	// An older binary catches up unbounded on its own; a newer one only at the rate it is given.
	if binary.Catchup && p.Catchup > p.Realtime {
		args = append(args, "-readrate_catchup", formatRate(p.Catchup))
	}
	return args
}

// formatRate formats realtime multiple for -readrate (at least one decimal place).
func formatRate(multiple float64) string {
	s := strconv.FormatFloat(multiple, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// formatSeconds spells a duration as ffmpeg's duration options take it: seconds, no unit suffix.
func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
}

// readArgs renders protocol-level fetch terms (deadline and reconnect).
func readArgs(p fetch.Policy) []string {
	var args []string
	if p.Deadline > 0 {
		args = append(args, "-rw_timeout", strconv.FormatInt(p.Deadline.Microseconds(), 10))
	}
	return append(args,
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", strconv.Itoa(int(fetch.BackoffMax.Seconds())),
		// Transient statuses are retried; any other refusal is the origin's answer.
		"-reconnect_on_http_error", "429,500,502,503,504",
	)
}

// demuxFlags are terms all inputs use (generate timestamps, drop corrupt packets).
var demuxFlags = []string{"-fflags", "+genpts+discardcorrupt"}

// sourceInputArgs renders each input with its fetch policy, headers, and pace.
func sourceInputArgs(source ProgramSource) []string {
	var args []string
	// A seam's jump is a discontinuity, not a gap (the default 10s stretches it); ffmpeg holds it for the whole read.
	if source.program.Seamed() {
		args = append(args, "-dts_delta_threshold", "1")
	}
	for _, input := range source.inputs() {
		args = append(args, demuxFlags...)
		args = append(args, paceArgs(input.fetch.Pace, source.binary)...)
		args = append(args, readArgs(input.fetch)...)
		args = append(args, ffmpeg.HeaderArgs(input.headers)...)
		args = append(args, ffmpeg.AdaptiveInputArgs(input.contentType, input.fetch.SegmentRetries)...)
		if input.offset != 0 {
			args = append(args, "-itsoffset", formatSeconds(input.offset))
		}
		args = append(args, "-i", input.url.String())
	}
	return args
}

// sourceMap renders one selected, kind-relative track.
func sourceMap(source ProgramSource, kind media.TrackKind) (string, bool) {
	track, ok := source.track(kind)
	if !ok {
		return "", false
	}
	spec := string(kind[:1])
	if kind == media.TrackVideo {
		spec = "V"
	}
	optional := ""
	if track.optional {
		optional = "?"
	}
	return fmt.Sprintf("%d:%s:%d%s", track.input, spec, track.index, optional), true
}

func sourceMapArgs(source ProgramSource) []string {
	var args []string
	if mapped, ok := sourceMap(source, media.TrackVideo); ok {
		args = append(args, "-map", mapped)
	}
	if mapped, ok := sourceMap(source, media.TrackAudio); ok {
		args = append(args, "-map", mapped)
	}
	return args
}
