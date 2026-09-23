package ffmpeg

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stupside/castor/internal/cast/policy/plan"
	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/container"
	"github.com/stupside/castor/internal/media"
)

// No "-c copy" keyword; decided track name goes on -c:v and -c:a (nowhere else).

type sourceInput struct {
	url         *url.URL
	headers     http.Header
	contentType string
	read        read.Policy
	offset      time.Duration
}

type sourceTrack struct {
	input    int
	index    int
	optional bool
}

// Command is an argv plus the extra output pipes it routes to, counted from the argv itself.
type Command struct {
	Args       []string
	ExtraPipes int
}

func command(args []string) Command {
	pipes := 0
	for _, arg := range args {
		rest, ok := strings.CutPrefix(arg, "pipe:")
		if !ok {
			continue
		}
		if fd, err := strconv.Atoi(rest); err == nil && fd >= firstExtraFD {
			pipes = max(pipes, fd-firstExtraFD+1)
		}
	}
	return Command{Args: args, ExtraPipes: pipes}
}

// EncodeInput is what an encode reads: the zero value reads nothing and is refused.
type EncodeInput struct {
	pipe   container.FormatInfo
	pace   read.Pace
	source ProgramSource
}

// FromPipe reads the container fed to stdin, at pace.
func FromPipe(format container.FormatInfo, pace read.Pace) EncodeInput {
	return EncodeInput{pipe: format, pace: pace}
}

// FromSource reads the network source on the terms of its read plan.
func FromSource(source ProgramSource) EncodeInput { return EncodeInput{source: source} }

func (in EncodeInput) piped() bool { return in.pipe.Muxer != "" }

type EncodeOptions struct {
	Input  EncodeInput
	Probe  media.ProbeInfo      // Measurement copy decisions were made from.
	Format container.FormatInfo // Container to produce.
	Video  plan.VideoTrack      // Video axis of encode (zero = no decision, refused).
	Audio  plan.AudioTrack      // Audio axis of encode (zero = no decision, refused).
}

// scaleFilter caps encode height while keeping aspect ratio and even dimensions.
func scaleFilter(maxHeight media.HeightCap) string {
	if maxHeight <= 0 {
		return ""
	}
	// -2 makes even width; round ceiling and odd height down (yuv420 rejects odd).
	evenCap := int(maxHeight) &^ 1
	return fmt.Sprintf("scale=-2:'min(%d,max(2,trunc(ih/2)*2))'", evenCap)
}

// statsPeriod is how often a pull reports on itself.
const statsPeriod = 500 * time.Millisecond

// paceArgs renders read pace as ffmpeg input flags (nil if unpaced).
func paceArgs(p read.Pace) []string {
	if p.Realtime <= 0 {
		return nil
	}
	return []string{
		"-readrate", formatRate(p.Realtime),
		"-readrate_initial_burst", strconv.Itoa(int(p.Burst.Seconds())),
	}
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
		args = append(args, paceArgs(input.read.Pace)...)
		args = append(args, readArgs(input.read)...)
		args = append(args, media.HeaderArgs(input.headers)...)
		args = append(args, media.AdaptiveInputArgs(input.contentType, input.read.SegmentRetries)...)
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

type axis struct {
	spec    string // ffmpeg's stream specifier: "v" or "a"
	copying bool
	refused bool // the tables already say this container will not carry it
}

var (
	encodedVideo = axis{spec: "v"}
	encodedAudio = axis{spec: "a"}
)

func copiedVideo(refused media.Axes) axis {
	return axis{spec: "v", copying: true, refused: refused.Video}
}

func copiedAudio(refused media.Axes) axis {
	return axis{spec: "a", copying: true, refused: refused.Audio}
}

func decidedTracks(video plan.VideoTrack, audio plan.AudioTrack) error {
	if !video.Decided() || !audio.Decided() {
		return fmt.Errorf("encode has an undecided axis (video decided: %t, audio decided: %t); a copy is a decision, not a default",
			video.Decided(), audio.Decided())
	}
	if venc, ok := video.Encode(); ok && (venc.Encoder.Name == "" || venc.Encoder.Codec == "") {
		return fmt.Errorf("video re-encode has no available encoder strategy")
	}
	return nil
}

// videoFilters is a re-encode's -vf chain, in the order the frames pass through it.
func videoFilters(venc plan.VideoEncode) []string {
	var filters []string
	if f := scaleFilter(venc.MaxHeight); f != "" {
		filters = append(filters, f)
	}
	if venc.SubtitleTextFile != "" {
		filters = append(filters, drawtextFilter(venc.SubtitleTextFile))
	}
	return append(filters, venc.Encoder.Filters...)
}

func videoEncodeArgs(venc plan.VideoEncode) []string {
	args := slices.Clone(venc.Encoder.Flags)
	if venc.Bitrate != "" {
		args = append(args, "-b:v", venc.Bitrate)
	}
	// A quality target rather than an average rate, which only the recovery floor asks for.
	if venc.Quality > 0 {
		args = append(args, "-crf", strconv.Itoa(venc.Quality))
	}
	// VBV cap: bound the instantaneous bitrate so the pacer's fixed send rate is a real ceiling.
	if venc.Maxrate != "" {
		args = append(args, "-maxrate", venc.Maxrate)
	}
	if venc.Bufsize != "" {
		args = append(args, "-bufsize", venc.Bufsize)
	}
	if venc.KeyframeIntervalSec > 0 {
		args = append(args, "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", venc.KeyframeIntervalSec))
	}
	return args
}

func hardwareInitArgs(video plan.VideoTrack) []string {
	venc, ok := video.Encode()
	if !ok {
		return nil
	}
	return venc.Encoder.InitArgs
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

func videoFilterArgs(video plan.VideoTrack) []string {
	venc, ok := video.Encode()
	if !ok {
		// A copy skips the filter chain entirely: there is nothing to filter in a copied bitstream.
		return nil
	}
	filters := videoFilters(venc)
	if len(filters) == 0 {
		return nil
	}
	return []string{"-vf", strings.Join(filters, ",")}
}

func encodeTuning(format container.FormatInfo) (container.Tuning, error) {
	if format.Muxer == "" {
		return container.Tuning{}, fmt.Errorf("no output container: EncodeOptions.Format is unset")
	}
	if format.Framing == media.FramingUnknown {
		return container.Tuning{}, fmt.Errorf("output container %q declares no framing", format.ContentType)
	}
	tuning := format.Tuning
	if tuning.Output == "" {
		return container.Tuning{}, fmt.Errorf("no container tuning for muxer %q", format.Muxer)
	}
	return tuning, nil
}

func encodeInputArgs(in EncodeInput) []string {
	if !in.piped() {
		return sourceInputArgs(in.source)
	}
	args := paceArgs(in.pace)
	args = append(args, demuxFlags...)
	return append(args, "-f", in.pipe.Muxer, "-i", "pipe:0")
}

// encodeMapArgs maps the first video and first audio track explicitly, and optionally.
func encodeMapArgs(in EncodeInput) []string {
	if in.piped() {
		return []string{"-map", "0:V:0?", "-map", "0:a:0?"}
	}
	return sourceMapArgs(in.source)
}

type axisArgs struct {
	args       []string
	movFlags   []string
	outputArgs []string
}

func adaptArgs(a axis, cp copyPlan, codec media.Codec, format container.FormatInfo) (axisArgs, error) {
	// A copy the tables already refuse must not be buildable.
	if a.copying && a.refused {
		return axisArgs{}, fmt.Errorf("copy of %q into %q on -c:%s is known not to be carriable; it should have been re-encoded",
			codec, format.ContentType, a.spec)
	}
	if !a.copying && len(cp.Filters) > 0 {
		return axisArgs{}, fmt.Errorf("re-encode to %q on -c:%s would carry bitstream filters %v; a repack belongs to a copy, so that row needs the copying predicate",
			codec, a.spec, cp.Filters)
	}
	out := axisArgs{movFlags: cp.MovFlags, outputArgs: cp.OutputArgs}
	// ffmpeg takes the whole chain as one comma separated value.
	if len(cp.Filters) > 0 {
		out.args = []string{"-bsf:" + a.spec, strings.Join(cp.Filters, ",")}
	}
	return out, nil
}

func videoCodecArgs(probe media.ProbeInfo, format container.FormatInfo, video plan.VideoTrack, refused media.Axes) (axisArgs, error) {
	venc, reencode := video.Encode()
	var (
		out axisArgs
		err error
	)
	if !reencode {
		out, err = adaptArgs(copiedVideo(refused), planVideoCopy(probe, format), probe.VideoCodec, format)
	} else {
		out, err = adaptArgs(encodedVideo, planVideoEncode(venc.Encoder.Codec, format), venc.Encoder.Codec, format)
	}
	if err != nil {
		return axisArgs{}, err
	}
	args := append([]string{"-c:v", video.Name()}, out.args...)
	if reencode {
		args = append(args, videoEncodeArgs(venc)...)
	}
	out.args = args
	return out, nil
}

func audioCodecArgs(probe media.ProbeInfo, format container.FormatInfo, audio plan.AudioTrack, refused media.Axes) (axisArgs, error) {
	aenc, reencode := audio.Encode()
	var (
		out axisArgs
		err error
	)
	if !reencode {
		out, err = adaptArgs(copiedAudio(refused), planAudioCopy(probe, format), probe.AudioCodec, format)
	} else {
		out, err = adaptArgs(encodedAudio, planAudioEncode(aenc.Codec, format), aenc.Codec, format)
	}
	if err != nil {
		return axisArgs{}, err
	}
	args := append([]string{"-c:a", audio.Name()}, out.args...)
	if reencode {
		args = append(args, audioEncodeArgs(aenc)...)
	}
	out.args = args
	return out, nil
}

func audioEncodeArgs(aenc plan.AudioEncode) []string {
	var args []string
	if aenc.SampleRate > 0 {
		args = append(args, "-ar", strconv.Itoa(aenc.SampleRate))
	}
	if aenc.Channels > 0 {
		args = append(args, "-ac", strconv.Itoa(aenc.Channels))
	}
	if aenc.Bitrate != "" {
		args = append(args, "-b:a", aenc.Bitrate)
	}
	return args
}

// trackArgs renders both decided axes into format: codecs first, then the output options their adaptations add.
func trackArgs(probe media.ProbeInfo, format container.FormatInfo, video plan.VideoTrack, audio plan.AudioTrack) (codecs, output []string, err error) {
	refused := container.Known(probe, format)
	v, err := videoCodecArgs(probe, format, video, refused)
	if err != nil {
		return nil, nil, err
	}
	a, err := audioCodecArgs(probe, format, audio, refused)
	if err != nil {
		return nil, nil, err
	}
	movFlags, err := movFlagsArgs(format.Muxer, format.Tuning.MovFlags, slices.Concat(v.movFlags, a.movFlags))
	if err != nil {
		return nil, nil, err
	}
	return slices.Concat(videoFilterArgs(video), v.args, a.args), slices.Concat(v.outputArgs, a.outputArgs, movFlags), nil
}

func movFlagsArgs(muxer string, base, extra []string) ([]string, error) {
	if len(base) == 0 && len(extra) > 0 {
		return nil, fmt.Errorf("copy adaptation contributed movflags %v to muxer %q, which declares none", extra, muxer)
	}
	if flags := slices.Concat(base, extra); len(flags) > 0 {
		return []string{"-movflags", "+" + strings.Join(flags, "+")}, nil
	}
	return nil, nil
}

func encodeOutputTarget(opts EncodeOptions, tuning container.Tuning, burnIn string) []string {
	args := []string{"-progress", pipeURL(progressFD)}
	if burnIn != "" {
		args = append(args, "-stats_period", "0.1")
	}
	if !opts.Input.piped() {
		args = append(args, opts.Input.source.outputArgs()...)
	}

	args = append(args, "-f", opts.Format.Muxer)
	args = append(args, tuning.Args...)
	return append(args, tuning.Output)
}

func EncodeArgs(opts EncodeOptions) (Command, error) {
	if !opts.Input.piped() && len(opts.Input.source.program.Inputs) == 0 {
		return Command{}, fmt.Errorf("encode has no input: build one with FromPipe or FromSource")
	}
	if err := decidedTracks(opts.Video, opts.Audio); err != nil {
		return Command{}, err
	}
	tuning, err := encodeTuning(opts.Format)
	if err != nil {
		return Command{}, err
	}
	codecs, output, err := trackArgs(opts.Probe, opts.Format, opts.Video, opts.Audio)
	if err != nil {
		return Command{}, err
	}

	// The burn-in is a property of the video re-encode, so it is empty on a copy by construction.
	var burnIn string
	if venc, ok := opts.Video.Encode(); ok {
		burnIn = venc.SubtitleTextFile
	}

	args := []string{"-hide_banner", "-nostats"}
	args = append(args, hardwareInitArgs(opts.Video)...)
	args = append(args, encodeInputArgs(opts.Input)...)
	args = append(args, encodeMapArgs(opts.Input)...)
	args = append(args, codecs...)
	args = append(args, "-strict", "-2")
	args = append(args, output...)
	return command(append(args, encodeOutputTarget(opts, tuning, burnIn)...)), nil
}

var SpoolFormat = spoolFormat()

func spoolFormat() container.FormatInfo {
	f, ok := container.FormatForContentType(media.MPEGTS)
	if !ok {
		panic("the format registry has no entry for " + media.MPEGTS)
	}
	return f
}

// PullOptions configures the single upstream reader's command line.
type PullOptions struct {
	Source ProgramSource

	// Probe is the measurement the floor's copy decisions were made from.
	Probe media.ProbeInfo

	// Video and Audio are the two axes of the read (see track.go).
	Video plan.VideoTrack
	Audio plan.AudioTrack

	Verbose bool

	PCM bool
	// PCMSampleRate is the audio sample rate for the PCM output.
	PCMSampleRate int
}

func pullReportArgs(verbose bool) []string {
	// Baseline "warning" and never "error".
	logLevel := "warning"
	if verbose {
		logLevel = "verbose"
	}

	// -progress on the runner's first extra pipe.
	return []string{"-nostats", "-loglevel", logLevel,
		"-progress", pipeURL(progressFD), "-stats_period", formatSeconds(statsPeriod)}
}

func pcmOutputArgs(opts PullOptions) []string {
	var args []string
	if audioMap, ok := sourceMap(opts.Source, media.TrackAudio); ok {
		args = append(args, "-map", audioMap)
	}
	args = append(args, "-vn")
	args = append(args, opts.Source.outputArgs()...)
	return append(args,
		"-ac", "1",
		"-ar", strconv.Itoa(opts.PCMSampleRate),
		"-f", "s16le", pipeURL(pcmFD),
	)
}

func PullArgs(opts PullOptions) (Command, error) {
	if err := decidedTracks(opts.Video, opts.Audio); err != nil {
		return Command{}, err
	}
	// A stream copy on each axis, or the floor encode the spool container needs.
	codecs, output, err := trackArgs(opts.Probe, SpoolFormat, opts.Video, opts.Audio)
	if err != nil {
		return Command{}, err
	}

	args := pullReportArgs(opts.Verbose)
	args = append(args, hardwareInitArgs(opts.Video)...)
	args = append(args, sourceInputArgs(opts.Source)...)
	args = append(args, sourceMapArgs(opts.Source)...)
	args = append(args, codecs...)
	args = append(args, output...)
	args = append(args, opts.Source.outputArgs()...)
	args = append(args, "-f", SpoolFormat.Muxer, "pipe:1")

	if opts.PCM {
		args = append(args, pcmOutputArgs(opts)...)
	}
	return command(args), nil
}

// drawtextFilter renders subtitle text bottom-centered with a translucent box.
func drawtextFilter(textFile string) string {
	return strings.Join([]string{
		"drawtext=textfile=" + escapeFilterArg(textFile),
		"reload=1",
		"fontsize=h/24",
		"fontcolor=white",
		"borderw=2",
		"bordercolor=black",
		"box=1",
		"boxcolor=black@0.45",
		"boxborderw=10",
		"text_align=center",
		"line_spacing=6",
		"x=(w-text_w)/2",
		"y=h-text_h-(h/20)",
	}, ":")
}

// escapeFilterArg escapes a value for ffmpeg's two-level filter-string parser.
func escapeFilterArg(s string) string {
	r := strings.NewReplacer(
		`\`, `\\\\`, // four backslashes in source → two in the arg → one survives both parsers
		`:`, `\\:`, // two backslashes + colon → one + colon after graph → literal colon after option parser
		`'`, `\\'`,
		`,`, `\\,`,
	)
	return r.Replace(s)
}
