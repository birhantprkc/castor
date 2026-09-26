package transcode

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

// ProgramSource binds execution-only read policies to inputs without leaking into media's source model.
type ProgramSource struct {
	program media.Program
	plan    read.Plan
	binary  ffmpeg.Binary
}

// NewProgramSource snapshots a program and its complete read plan.
func NewProgramSource(program media.Program, plan read.Plan, binary ffmpeg.Binary) (ProgramSource, error) {
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

	return ProgramSource{program: program.Clone(), plan: plan.Clone(), binary: binary}, nil
}

func (s ProgramSource) inputs() []sourceInput {
	inputs := make([]sourceInput, 0, len(s.program.Inputs))
	for _, input := range s.program.Inputs {
		inputs = append(inputs, sourceInput{
			url:         input.URL,
			headers:     input.Headers,
			contentType: input.ContentType,
			read:        s.plan[input.ID],
			offset:      s.program.Offsets[input.ID],
			spliced:     input.Fetch.Seamed(),
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
		args := readArgs(input.read)
		args = append(args, ffmpeg.HeaderArgs(input.headers)...)
		args = append(args, ffmpeg.AdaptiveInputArgs(input.contentType, input.read.SegmentRetries)...)
		out = append(out, ffmpeg.ProbeInput{ID: s.program.Inputs[i].ID, URL: input.url.String(), Args: args})
	}
	return out
}

type sourceInput struct {
	url         *url.URL
	headers     http.Header
	contentType string
	read        read.Policy
	offset      time.Duration
	spliced     bool
}

type sourceTrack struct {
	input    int
	index    int
	optional bool
}

// paceArgs renders read pace as ffmpeg input flags (nil if unpaced).
func paceArgs(p read.Pace, binary ffmpeg.Binary) []string {
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
func readArgs(p read.Policy) []string {
	var args []string
	if p.Deadline > 0 {
		args = append(args, "-rw_timeout", strconv.FormatInt(p.Deadline.Microseconds(), 10))
	}
	if p.Backoff > 0 {
		args = append(args,
			"-reconnect", "1",
			"-reconnect_streamed", "1",
			"-reconnect_delay_max", strconv.Itoa(int(p.Backoff.Seconds())),
		)
		if len(p.RetryStatuses) > 0 {
			args = append(args, "-reconnect_on_http_error", formatStatuses(p.RetryStatuses))
		}
	}
	return args
}

func formatStatuses(codes []int) string {
	out := make([]string, len(codes))
	for i, code := range codes {
		out[i] = strconv.Itoa(code)
	}
	return strings.Join(out, ",")
}

// demuxFlags are terms all inputs use (generate timestamps, drop corrupt packets).
var demuxFlags = []string{"-fflags", "+genpts+discardcorrupt"}

// sourceInputArgs renders each input with its fetch policy, headers, and pace.
func sourceInputArgs(source ProgramSource) []string {
	var args []string
	for _, input := range source.inputs() {
		args = append(args, demuxFlags...)
		args = append(args, paceArgs(input.read.Pace, source.binary)...)
		args = append(args, readArgs(input.read)...)
		args = append(args, ffmpeg.HeaderArgs(input.headers)...)
		args = append(args, ffmpeg.AdaptiveInputArgs(input.contentType, input.read.SegmentRetries)...)
		if input.offset != 0 {
			args = append(args, "-itsoffset", formatSeconds(input.offset))
		}
		// A seam's jump is a discontinuity, not a gap (default 10s stretches it); the option is global, so it holds for every input.
		if input.spliced {
			args = append(args, "-dts_delta_threshold", "1")
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
