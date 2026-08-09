// spool lands with a track missing and the encode then dies mapping a stream
// that is not there. Which axes those are is the caller's answer, taken from a
// probe of the source before this ran, because a download cannot un-write what
// it already put on disk.
package ffmpeg

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/media"
)

// NetworkSource is an upstream castor reads over the network: the URL plus
// everything a fetch of it needs to succeed and behave. Castor has exactly two
// network readers (the spool puller and the served remux) and both describe
// their upstream with this one type, so the whole input side of their command
// lines is assembled by inputArgs and cannot drift apart.
type NetworkSource struct {
	URL *url.URL

	// AudioURL is the companion audio rendition of a demuxed program, read as a
	// second input alongside URL. nil is the ordinary muxed case.
	AudioURL *url.URL

	// Headers are the HTTP request headers ffmpeg sends when fetching URL
	// (Referer, Origin, Cookie, User-Agent: what a proxied CDN gates on).
	Headers http.Header

	// ContentType is the source's container. It selects the container-specific
	// input flags (see containerInputArgs) and whether the source is fetched as
	// segments (see segmented).
	ContentType string

	// Read is how this upstream is fetched: the mid-read deadline, the reconnect
	// terms, and the pace the source's nature calls for. It is carried as a value
	// chosen from what the source published (see read.For) rather than decided here,
	// because a rule about a hostile origin is not a property of an argument builder.
	// Everything below renders it; nothing below chooses it.
	Read read.Policy
}

// NewNetworkSource describes a resolved stream as an upstream to read on the given
// terms. Both readers build their source through it, so a pull and a remux of the
// same stream fetch it identically.
func NewNetworkSource(stream *media.Stream, policy read.Policy) NetworkSource {
	return NetworkSource{
		URL:         stream.URL,
		AudioURL:    stream.AudioURL,
		Headers:     stream.Headers,
		ContentType: stream.ContentType,
		Read:        policy,
	}
}

// EncodeOptions is the full description of an encode invocation. Every choice
// is explicit; nothing is inferred from globals or context. The planner
// upstream is responsible for filling these in based on device capabilities
// and source media properties.
type EncodeOptions struct {
	// PipeFormat is the container fed to stdin, zero for a network input. The
	// caller feeds the bytes via WithStdin and takes the record from the container
	// it is actually feeding (SpoolFormat, the only one today). Used to encode from
	// the local spool: pipes never report EOF until the writer closes, which is
	// what lets ffmpeg consume a still-growing stream.
	//
	// It carries the whole format record rather than a muxer name, and the zero
	// value rather than an empty string is what says "no pipe input". The record is
	// not decoration: the MPEG-TS spool re-frames everything through it, so an fMP4
	// source's already-out-of-band AAC comes back off the spool as ADTS, and the
	// spool-fed encode needs a repack that a direct remux of the same source does
	// not. Nothing reads its Framing today, since the repack is keyed on the
	// destination, but having the input container's properties out of reach is what
	// made that asymmetry hard to see.
	PipeFormat media.FormatInfo

	// Source is the network input, read when PipeFormat is zero and ignored
	// otherwise.
	Source NetworkSource

	// Probe is the measurement every copy decision in this command line was made
	// from: the codecs of the tracks the maps below actually select. It is the
	// single carrier, read both by the resolvers that chose copy-vs-encode and by
	// the adaptation planner that fills in what a copy needs, so the two can never
	// be looking at different facts.
	//
	// It must describe the MAPPED tracks. On a demuxed program the audio half comes
	// from the second input, so SourceProber fills it from the audio rendition or
	// leaves it zero; a zero audio half means "no measurement", which matches no
	// adaptation and which the resolver reads as "re-encode", both of which are the
	// safe answers.
	Probe media.ProbeInfo

	// Format is the container to produce, as the registry describes it: the muxer
	// to run, how the result is delivered (a segmented format writes a playlist
	// plus rolling segments into the process working directory, see WithWorkDir,
	// rather than a single stream on pipe:1), and how it frames the streams inside
	// it. Carrying the whole record rather than a muxer name is what lets a copy
	// know what the destination will accept without recognising it by name.
	Format media.FormatInfo

	// Video and Audio are the two axes of the encode: each is a stream copy or a
	// re-encode carrying its own parameters (see track.go). Both must be decided
	// before this reaches EncodeArgs; the zero value is not a copy, it is the
	// absence of a decision, and it is refused.
	Video VideoTrack
	Audio AudioTrack
}

// scaleFilter caps an encode's height while keeping the aspect ratio and an even width
// (an encoder requirement, which is what -2 answers), or nothing at all where no ceiling
// was given. Zero is no ceiling, matching core.Resolve's own convention.
//
// One expression, shared by every producer of a re-encode castor runs. The two that exist
// (the served encode and the pull's floor) are both asked for a picture at whatever
// resolution the source happens to be, on hardware nobody chose, and a producer that
// skipped the cap asks a veryfast software encoder for 3840x2160 in realtime. It does not
// hold it, and a read that cannot hold realtime is one the deliverability judgement
// convicts as a starving source (speed=0.0627 is what that looks like).
func scaleFilter(maxHeight media.HeightCap) string {
	if maxHeight <= 0 {
		return ""
	}
	return fmt.Sprintf("scale=-2:'min(%d,ih)'", maxHeight)
}

// containerInputArgs returns the ffmpeg input flags a source container needs, plus the
// terms of the read that only that container's demuxer understands.
//
// HLS (and DASH) playlists require the extension checks relaxed, and a segment whose open
// failed is re-fetched rather than skipped. Both are options on the HLS demuxer, so ffmpeg
// aborts a plain-file input (MP4, MKV) with "Option not found" and reads nothing at all when
// either is present: "-seg_max_retry 3 -i in.mp4" exits before opening the file. Direct files
// need none, and a policy carrying a segment retry budget for a source that turns out not to
// be a playlist renders none here rather than failing the read.
func containerInputArgs(contentType string, p read.Policy) []string {
	switch contentType {
	case media.HLS:
		args := slices.Clone(media.HLSInputArgs)
		if p.SegmentRetries > 0 {
			args = append(args, "-seg_max_retry", strconv.Itoa(p.SegmentRetries))
		}
		return args
	default:
		return nil
	}
}

// paceHLSWindow is a pace imposed by an OUTPUT rather than by a source, which is
// why it is the one pace this package holds: it feeds a deleting HLS window (see
// containerTuning) at exactly wall-clock speed rather than the read policy's
// over-speed, because the client consumes that window at 1x and anything faster
// rolls the next-needed segment off the back before it asks. The burst fills the
// window once so the device can prebuffer first.
var paceHLSWindow = read.Pace{Realtime: 1.0, Burst: hlsWindowSeconds * time.Second}

// paceArgs renders a read pace as ffmpeg input flags, or nothing at all when the
// read is unpaced.
func paceArgs(p read.Pace) []string {
	if p.Realtime <= 0 {
		return nil
	}
	return []string{
		"-readrate", formatRate(p.Realtime),
		"-readrate_initial_burst", strconv.Itoa(int(p.Burst.Seconds())),
	}
}

// formatRate spells a realtime multiple the way -readrate takes it, keeping at least
// one decimal place. "2" and "2.0" are the same multiple to ffmpeg, and the decimal
// is what makes a command line read as a rate rather than as a count.
func formatRate(multiple float64) string {
	s := strconv.FormatFloat(multiple, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// formatSeconds spells a duration the way ffmpeg's own duration options take it, in
// seconds with no unit suffix and no trailing zeros.
func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
}

// readArgs renders the protocol-level fetch terms of a read policy: the mid-read deadline
// and the reconnect block. A policy with no backoff ceiling renders no reconnect flags at
// all, including the retry status set, because a list of statuses to reconnect on
// says nothing when reconnecting is off. A policy with no deadline renders no -rw_timeout,
// which is the fragile read's whole point and not an omission: ffmpeg's default is no
// deadline, so what is withheld here is withheld from the reader too.
//
// The segment retry budget is not rendered here, because it is an option of one demuxer
// rather than of the protocol (see containerInputArgs).
//
// SourceProber renders the same terms, which is what makes its claim to open the
// source exactly as the reader will true of the deadline and the retries and not
// only of the request headers. The pace is deliberately not part of this: -readrate
// is an option of the ffmpeg CLI and not of ffprobe, and a probe reads a few leading
// packets rather than a title, so there is nothing for it to outrun.
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

// formatStatuses renders a retry status set as the one comma separated value
// -reconnect_on_http_error takes.
func formatStatuses(codes []int) string {
	out := make([]string, len(codes))
	for i, code := range codes {
		out[i] = strconv.Itoa(code)
	}
	return strings.Join(out, ",")
}

// segmented reports whether reading this source means many small segment
// requests rather than one long GET, which is what decides whether a reader free
// to run at wire speed should pace anyway: a two-hour HLS title read unpaced is
// thousands of requests in a couple of minutes, and CDNs answer that with 429s.
// One long GET of a single file (mp4/mkv/avi) is throttled by nothing.
//
// It is answered from the container this builder is about to open, which is the
// fact a builder has. What pace to use once the answer is yes belongs to the read
// policy, which took it from what the source published.
func (s NetworkSource) segmented() bool { return s.ContentType == media.HLS }

// inputArgs renders the input side of a network read: the pacing, the read
// policy's fetch terms, the request headers, the container's input flags, and the
// URL. A demuxed program is two inputs read on identical terms, since its
// renditions are two halves of one program from one origin (see audioMap for the
// mapping that follows).
func (s NetworkSource) inputArgs(pace read.Pace) []string {
	args := s.input(pace, s.URL)
	if s.AudioURL != nil {
		args = append(args, s.input(pace, s.AudioURL)...)
	}
	return args
}

// input renders the flags for one of the source's inputs. ffmpeg applies these
// to the input that follows them, so every input of a demuxed program repeats
// them.
func (s NetworkSource) input(pace read.Pace, u *url.URL) []string {
	args := paceArgs(pace)
	args = append(args, readArgs(s.Read)...)
	args = append(args, media.HeaderArgs(s.Headers)...)
	args = append(args, containerInputArgs(s.ContentType, s.Read)...)
	return append(args, "-i", u.String())
}

// audioMap is the ffmpeg stream specifier for the source's audio: the second
// input when the program is demuxed, the first input's own audio otherwise. The
// video map is always 0:v:0, so this is the only specifier that moves.
//
// A pipe-fed encode leaves the source zero-valued and lands on 0:a:0, which is
// right: the spool it reads is a single muxed stream whatever the origin looked
// like.
// The "?" suffix is the output-side optional-stream marker (see EncodeArgs): a
// source with no audio track must produce a valid cast, not an argument-parse
// failure.
func (s NetworkSource) audioMap() string {
	if s.AudioURL != nil {
		return "1:a:0?"
	}
	return "0:a:0?"
}

// axis is which half of the program a set of adaptations applies to, and how it
// is being produced. It exists so the two facts that travel together, the ffmpeg
// stream specifier and whether this half is a copy, cannot be passed separately
// and contradict each other.
type axis struct {
	spec    string // ffmpeg's stream specifier: "v" or "a"
	copying bool
	refused bool // the tables already say this container will not carry it
}

var (
	encodedVideo = axis{spec: "v"}
	encodedAudio = axis{spec: "a"}
)

func copiedVideo(refused carriage.Axes) axis {
	return axis{spec: "v", copying: true, refused: refused.Video}
}

func copiedAudio(refused carriage.Axes) axis {
	return axis{spec: "a", copying: true, refused: refused.Audio}
}

// EncodeArgs assembles the encode command line. No "magic" flags: every argument
// is either part of the standard input/output setup or comes straight from a
// field in EncodeOptions.
//
// It validates one thing, that both axes were decided. The cross-field contracts
// it used to police (a burn-in without an encoder, an empty "-c:a") are no longer
// states an EncodeOptions can hold, so the only way to reach here with something
// unbuildable is to not have decided at all, and a decision nobody took must not
// silently become a stream copy: that is how an axis nothing planned reaches a
// muxer as "-c:v copy" and is discovered from the artifact rather than here.
func EncodeArgs(opts EncodeOptions) ([]string, error) {
	if !opts.Video.Decided() || !opts.Audio.Decided() {
		return nil, fmt.Errorf("encode has an undecided axis (video decided: %t, audio decided: %t); a copy is a decision, not a default",
			opts.Video.Decided(), opts.Audio.Decided())
	}
	// Unwrapped once: every branch below asks the same two questions, and asking
	// them once is what keeps "is this axis re-encoded" and "what does the
	// re-encode say" from being two independent readings that can disagree.
	venc, reencodeVideo := opts.Video.Encode()
	aenc, reencodeAudio := opts.Audio.Encode()

	// The burn-in is a property of the video re-encode, so it is read from there
	// and is empty on a copy by construction.
	var burnIn string
	if reencodeVideo {
		burnIn = venc.SubtitleTextFile
	}

	if opts.Format.Muxer == "" {
		return nil, fmt.Errorf("no output container: EncodeOptions.Format is unset")
	}
	// A container that has not declared how it frames its streams cannot be
	// encoded into, because every copy decision below is a function of that
	// declaration and the zero value would silently answer "in band". This catches
	// a FormatInfo built anywhere but the registry, and it catches a resolver that
	// ran before Format was populated. Both used to produce the original bug back
	// with no error at all.
	if opts.Format.Framing == media.FramingUnknown {
		return nil, fmt.Errorf("output container %q declares no framing", opts.Format.ContentType)
	}
	tuning, ok := containerTuning[opts.Format.Muxer]
	if !ok {
		return nil, fmt.Errorf("no container tuning for muxer %q", opts.Format.Muxer)
	}

	// -nostats: the \r-terminated progress line never completes, so it
	// accumulates into one giant stderr "line" that drowns the tail buffer
	// real errors live in. Position tracking uses -progress instead.
	args := []string{"-hide_banner", "-nostats", "-fflags", "+genpts+discardcorrupt"}

	// A re-encode contributes its encoder's own hardware-device setup (emitted
	// before the input, so both the upload filter and the encoder can reference
	// it), filters, and flags, so there is no per-encoder branching below.
	if reencodeVideo {
		args = append(args, venc.Encoder.InitArgs...)
	}

	if opts.PipeFormat.Muxer != "" {
		if burnIn != "" {
			// Pace the encode to just above realtime. It must stay near
			// wall-clock speed: the cue writer swaps drawtext's textfile as
			// -progress ticks arrive, and unpaced the encoder rips through the
			// spool at CPU speed (every tick covering seconds of video, so cues
			// smear or skip) and overtakes the transcriber's commit frontier,
			// after which every cue lookup misses and subtitles stop. It must
			// not be exactly realtime either: see read.EncodePace.
			args = append(args, paceArgs(read.EncodePace)...)
		}
		args = append(args, "-f", opts.PipeFormat.Muxer, "-i", "pipe:0")
	} else {
		// This reader may run at wire speed, since its output is either
		// replay-spooled from byte 0 or a rolling window, so it paces only where
		// running fast would hurt:
		//
		//   - a deleting HLS output window (see containerTuning) must be produced at
		//     exactly wall-clock speed, or the window rolls segments off faster than
		//     the device plays them at 1x and only the tail is ever fetchable; its
		//     burst fills the window once so the device can prebuffer first;
		//   - a segmented source is read at the pace its read policy calls for, since
		//     wire speed against a segmented CDN is a request storm (see segmented).
		var pace read.Pace
		switch {
		case opts.Format.Delivery == media.DeliverSegmented:
			pace = paceHLSWindow
		case opts.Source.segmented():
			pace = opts.Source.Read.Pace
		}
		args = append(args, opts.Source.inputArgs(pace)...)
	}

	// Map the first video and first audio track explicitly, and optionally.
	//
	// Explicitly, because ffmpeg's default stream selection picks the audio track
	// with the most channels, which on a multi-track source differs from the first
	// track the planner probed. On a multi-track source -map 0:a:0 and ffprobe's
	// first audio stream agree, while the default selection reaches past both for
	// the 5.1 track. Pinning the pair keeps the encoded track identical to the
	// probed one, and on a demuxed program it joins the two inputs into one output.
	//
	// Optionally (the "?" suffix), because a pinned map turns a missing track into
	// an argument-parse failure before a single byte is read, which is how an
	// audio-only source, a video-only source and a video-only HLS playlist alike
	// used to die. With the suffix each of them produces valid output, and on a
	// source carrying both tracks the behaviour is unchanged. A stray -bsf
	// against a map that matched nothing is silently ignored at exit 0, so the
	// relaxation creates no new failure mode. This is the cheapest way castor
	// honours "never reject a source".
	//
	// Optionality stops at the output side. A demuxed program whose audio rendition
	// 404s fails at input-open (exit 8, "Error opening input files") whether the
	// map is 1:a:0 or 1:a:0?, because the suffix is an output-side relaxation.
	// Degrading that shape means building a command line without the second -i, not
	// relaxing a map.
	args = append(args, "-map", "0:v:0?", "-map", opts.Source.audioMap())

	// Video filter chain. scale= runs first so text is rendered at the final
	// resolution (crisper than scaling rendered text); it caps height while
	// keeping width divisible by 2 (encoder requirement) and preserving aspect
	// ratio via -2. The encoder's own filters (e.g. the VA-API GPU upload) come
	// last, after scale and drawtext have run on CPU frames. A copy skips all of
	// this, and cannot ask for any of it: every filter below reads a field that
	// only exists inside VideoEncode, because a copied bitstream can't be filtered.
	var vfilters []string
	if reencodeVideo {
		if f := scaleFilter(venc.MaxHeight); f != "" {
			vfilters = append(vfilters, f)
		}
		if venc.SubtitleTextFile != "" {
			vfilters = append(vfilters, drawtextFilter(venc.SubtitleTextFile))
		}
		vfilters = append(vfilters, venc.Encoder.Filters...)
	}
	if len(vfilters) > 0 {
		args = append(args, "-vf", strings.Join(vfilters, ","))
	}

	// Both branches ask the adaptation tables what the destination needs, because a
	// muxer's rules are about the bitstream it receives whoever produced it. The
	// plan is computed here from opts.Probe and opts.Format rather than handed in as
	// a field, so a filter cannot be set independently of the track it belongs to.
	//
	// What the tables already know this destination will not carry is asked once for
	// the whole command line, because the answer is a function of two things that do
	// not change inside it: the probe of the mapped tracks and the output format.
	refused := carriage.Known(opts.Probe, opts.Format)

	var extraMovFlags, extraOutputArgs []string

	// adapt applies one axis's copy adaptations. The axis is a value rather than a
	// pair of booleans and a stream-specifier string: each of the four call sites
	// knows statically which axis it is and whether it copies, and spelling that as
	// arguments meant two things that could disagree about the same fact.
	adapt := func(a axis, plan copyPlan, codec media.Codec) error {
		// A copy the tables already refuse must not be buildable. The resolver is
		// supposed to have asked carriage and chosen a re-encode; if it did not,
		// failing here beats handing an MPEG-TS muxer a track it will write as private
		// data and report success for. It consults the owner rather than keeping a
		// second opinion, so one place still knows which pairs do not work.
		if a.copying && a.refused {
			return fmt.Errorf("copy of %q into %q on -c:%s is known not to be carriable; it should have been re-encoded",
				codec, opts.Format.ContentType, a.spec)
		}
		// A repack rewrites framing a track ALREADY has, so it is meaningless on one
		// castor is producing and fatal when the filter does not know the codec. Every
		// row carrying Filters is gated on the copying predicate; this is the assertion
		// that a future row cannot quietly forget it.
		if !a.copying && len(plan.Filters) > 0 {
			return fmt.Errorf("re-encode to %q on -c:%s would carry bitstream filters %v; a repack belongs to a copy, so that row needs the copying predicate",
				codec, a.spec, plan.Filters)
		}
		// ffmpeg takes the whole chain as one comma separated value, which is why the
		// plan accumulates a slice rather than emitting a flag per adaptation.
		if len(plan.Filters) > 0 {
			args = append(args, "-bsf:"+a.spec, strings.Join(plan.Filters, ","))
		}
		extraMovFlags = append(extraMovFlags, plan.MovFlags...)
		extraOutputArgs = append(extraOutputArgs, plan.OutputArgs...)
		return nil
	}

	args = append(args, "-c:v", opts.Video.Name())
	if !reencodeVideo {
		if err := adapt(copiedVideo(refused), planVideoCopy(opts.Probe, opts.Format), opts.Probe.VideoCodec); err != nil {
			return nil, err
		}
	} else {
		// The muxer's rules apply to what castor produces too, so the encoder's
		// output codec goes through the same table (an encoded HEVC track needs the
		// hvc1 tag exactly like a copied one). A produced codec is never uncarriable,
		// which is what makes the ladder terminal, so this cannot block.
		if err := adapt(encodedVideo, planVideoEncode(venc.Encoder.Codec, opts.Probe, opts.Format), venc.Encoder.Codec); err != nil {
			return nil, err
		}
		args = append(args, venc.Encoder.Flags...)
		if venc.Bitrate != "" {
			args = append(args, "-b:v", venc.Bitrate)
		}
		// VBV cap: bound the instantaneous bitrate so the pacer's fixed send
		// rate is a real ceiling. Both encoders honour this (libx264 VBV,
		// VideoToolbox/VA-API DataRateLimits).
		if venc.Maxrate != "" {
			args = append(args, "-maxrate", venc.Maxrate)
		}
		if venc.Bufsize != "" {
			args = append(args, "-bufsize", venc.Bufsize)
		}
		// Cap the GOP in wall-clock time, fps-independent, so a renderer that
		// joins mid-stream resyncs within the interval. Works on every encoder
		// family (VideoToolbox additionally needs -g in its Flags to lift its
		// wasteful sub-second default so this expression is the real limiter).
		if venc.KeyframeIntervalSec > 0 {
			args = append(args, "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", venc.KeyframeIntervalSec))
		}
	}

	args = append(args, "-c:a", opts.Audio.Name())
	if !reencodeAudio {
		if err := adapt(copiedAudio(refused), planAudioCopy(opts.Probe, opts.Format), opts.Probe.AudioCodec); err != nil {
			return nil, err
		}
	} else {
		// Same on the audio side, and the one that matters most: the Dolby rung of
		// core.DecideAudio hands the mp4 muxer an E-AC-3 track it refuses to write a
		// header for unless delay_moov is set, whether castor copied that track or
		// encoded it.
		if err := adapt(encodedAudio, planAudioEncode(aenc.Codec, opts.Probe, opts.Format), aenc.Codec); err != nil {
			return nil, err
		}
		if aenc.SampleRate > 0 {
			args = append(args, "-ar", strconv.Itoa(aenc.SampleRate))
		}
		if aenc.Channels > 0 {
			args = append(args, "-ac", strconv.Itoa(aenc.Channels))
		}
		if aenc.Bitrate != "" {
			args = append(args, "-b:a", aenc.Bitrate)
		}
	}

	// -strict -2 unconditionally, and as an OUTPUT option. It is a no-op for every
	// codec but one, and the only thing that unlocks a copied TrueHD track into mp4
	// and hls/fMP4, which the muxer otherwise refuses as experimental. Placement
	// matters: given before -i it never reaches the muxer and the encode still dies.
	// Since it costs nothing elsewhere, gating it on a codec would buy only a codec
	// list that goes stale. It also relaxes the encoder side, which changes nothing here
	// because the only encoders castor selects are h264, hevc, aac, ac3 and eac3,
	// none of which ffmpeg marks experimental.
	args = append(args, "-strict", "-2")
	args = append(args, extraOutputArgs...)

	// -movflags is an AVOption, so the last one on the command line wins: an
	// adaptation's tokens have to be merged into the muxer's base rather than
	// emitted as a second flag that would clobber it. A muxer with no base and an
	// adaptation that wants tokens is a mis-scoped predicate, not a runtime
	// condition, so it is reported rather than silently dropped.
	if len(tuning.MovFlags) == 0 && len(extraMovFlags) > 0 {
		return nil, fmt.Errorf("copy adaptation contributed movflags %v to muxer %q, which declares none", extraMovFlags, opts.Format.Muxer)
	}
	if flags := slices.Concat(tuning.MovFlags, extraMovFlags); len(flags) > 0 {
		args = append(args, "-movflags", "+"+strings.Join(flags, "+"))
	}

	// -progress on the runner's first extra pipe, on every encode. It is this
	// process's only machine-readable output about itself: its position, the bytes it
	// has produced, and the speed it is producing them at, which is what separates an
	// encoder that is slow from one that is starving behind a slow read. Emitting it
	// only when subtitles were being burned is why the encode legs of a cast that died
	// after 935300 bytes could be described only by the byte count of what it wrote.
	//
	// The reader is opened by whoever starts the process (see WithExtraPipes) and must
	// drain it for the life of the encode.
	args = append(args, "-progress", pipeURL(progressFD))
	if burnIn != "" {
		// A burn-in makes the report period the cue placement granularity in video
		// time: the cue writer swaps drawtext's textfile as these blocks arrive, and the
		// encode is paced at realtime, so ffmpeg's default half second would place every
		// line up to half a second late.
		args = append(args, "-stats_period", "0.1")
	}

	args = append(args, "-f", opts.Format.Muxer)
	args = append(args, tuning.Args...)
	return append(args, tuning.Output), nil
}

// containerTuningEntry is the fixed output configuration of one muxer.
type containerTuningEntry struct {
	// MovFlags is the base -movflags token set, nil for a muxer with no such
	// option. Copy adaptations merge their own tokens into it (see
	// copyAdaptation.MovFlags); a plan contributing tokens to a muxer whose base is
	// nil is a programming error and EncodeArgs says so.
	MovFlags []string
	// Args are the muxer's fixed output options, emitted after -f <muxer>.
	Args []string
	// Output is the muxer's output target: the pipe for a single growing stream,
	// the playlist filename for a segmented one. It lives here rather than being
	// derived from DeliveryKind so a second segmented format would declare its own
	// manifest name instead of silently inheriting HLS's.
	Output string
}

// containerTuning is the fixed output configuration of each muxer castor writes:
// the options that are true of the container regardless of what is inside it. The
// per-codec options live in the copy adaptation tables instead, because those
// depend on the bitstream and these do not.
//
// It is keyed by the ffmpeg muxer name declared on the format registry row, and a
// muxer with no entry is an error rather than a silent no-tuning fall-through.
// That is deliberate: the mp4 entry is what lets a fragmented output exist on a
// pipe at all, and the previous shape (a switch with no default) meant renaming
// the registry's mp4 row to "mov" would silently drop +frag_keyframe+empty_moov
// and break every mp4 cast on non-seekable output with no error anywhere.
var containerTuning = map[string]containerTuningEntry{
	media.MuxerMPEGTS: {
		// mpegts container tuning, reduced to the two options that do anything.
		//
		// -muxdelay 0 -muxpreload 0 pull the output's start_time back to zero from
		// the demuxer's own offset, for about 2.2% in size, with packet counts
		// unchanged and a clean decode.
		//
		// Two options that used to be here are gone because they were dead.
		// -pat_period 0.1 is already ffmpeg's default, and -mpegts_flags
		// +resend_headers only re-emits PAT/PMT at explicit segment boundaries while
		// castor's mpegts output is always one pipe; neither changed the output. If castor ever adds a segmented mpegts format, resend_headers comes
		// back with it, and so does the Framing question on that row.
		Args:   []string{"-mpegts_flags", "+initial_discontinuity", "-muxdelay", "0", "-muxpreload", "0"},
		Output: "pipe:1",
	},
	media.MuxerMP4: {
		// Plain mp4 needs a seekable output to finalize its moov atom; on a pipe it
		// must be fragmented instead. empty_moov puts the initialization segment out
		// out immediately, which is the whole point of it: the client holds ftyp and
		// moov within milliseconds, while every alternative configuration makes it
		// wait for the first fragment.
		//
		// It is not free. empty_moov clears AVFMT_FLAG_AUTO_BSF, which is the
		// machinery that would otherwise insert aac_adtstoasc for us: at -v verbose
		// the muxer prints "Empty MOOV enabled; disabling automatic bitstream
		// filtering" and then "Malformed AAC bitstream detected", in that order, and
		// the first line is the cause of the second. That is why the AAC repack is
		// hand-written rather than inherited, and it is a consequence of this flag
		// rather than a fact about AAC. Dropping empty_moov would restore the
		// automatic repack and fix AC-3 at the same time (15/15 source-codec
		// combinations with an empty filter table), and it is not done for three
		// reasons: the hls muxer exposes no check_bitstream of its own and ignores
		// -movflags entirely, so Roku would still need the hand-written filter and
		// would silently regress if the design leaned on the automatic one; the first
		// media chunk completes at t+3.63 s instead of t+1.58 s; and the moov's shape
		// changes (1241 to 2503 bytes, a real fragment-1 sample table, mvhd duration
		// bounded rather than 0) in a way no local test can clear against a real
		// receiver.
		MovFlags: []string{"frag_keyframe", "empty_moov", "default_base_moof"},
		Output:   "pipe:1",
	},
	media.MuxerHLS: {
		// A live sliding-window fMP4 tail: hlsListSize segments of
		// ~hlsSegmentSeconds each keep hlsWindowSeconds on disk, comfortably above
		// the ~30s-behind-live-edge window HLS clients conventionally buffer;
		// hls_playlist_type stays unset so the window rolls (event/vod would pin the
		// list size to 0 and grow disk unbounded).
		//
		// -hls_segment_type fmp4 is the flag the HLS registry row's FramingOutOfBand
		// is about. The two are one fact stored in two places and must move together:
		// the same muxer with -hls_segment_type mpegts carries ADTS AAC untouched, so
		// declaring OutOfBand while writing mpegts segments would hand the repack to
		// an in-band destination, which exits cleanly having discarded almost every
		// audio packet. TestHLSSegmentTypeMatchesDeclaredFraming binds them.
		//
		// The bare relative filenames rely on the process running WithWorkDir.
		Args: []string{
			"-hls_time", strconv.Itoa(hlsSegmentSeconds),
			"-hls_list_size", strconv.Itoa(hlsListSize),
			"-hls_flags", "delete_segments+independent_segments",
			"-hls_segment_type", "fmp4",
			"-hls_fmp4_init_filename", media.HLSInitName,
			"-hls_segment_filename", media.HLSSegmentPattern,
		},
		Output: media.HLSPlaylistName,
	},
}

const (
	// hlsSegmentSeconds targets the segment length. With stream-copy the muxer can
	// only cut on a source keyframe, so it is a lower bound, not exact.
	hlsSegmentSeconds = 4
	// hlsListSize is how many segments the rolling playlist keeps on disk.
	hlsListSize = 8
	// hlsWindowSeconds is the on-disk window; it also sizes paceHLSWindow's
	// burst so the device can prebuffer one full window before pacing binds.
	hlsWindowSeconds = hlsSegmentSeconds * hlsListSize
)

// HLSWindow is all the media a segmented delivery ever has in hand, because
// delete_segments removes everything behind it. It is exported for the party that has to
// know what a renderer can still be handed: a judgement that counted media this muxer has
// already deleted would hold a cast open over segments that answer 404.
const HLSWindow = hlsWindowSeconds * time.Second

// SpoolFormat is the container the read-once path passes through itself: the
// pull muxes the upstream into it (PullArgs), it lands in the spool file on
// disk, and the encode demuxes it back off stdin (EncodeOptions.PipeFormat).
// MPEG-TS, because it is strictly append-only (no trailer, no seeking back to
// patch a header), which is what lets a tail read the file while it is still
// growing. Declared once so the two ends of that pipe, and the file between
// them, cannot disagree about what is in it.
var SpoolFormat = spoolFormat()

func spoolFormat() media.FormatInfo {
	f, ok := media.FormatForContentType(media.MPEGTS)
	if !ok {
		panic("the format registry has no entry for " + media.MPEGTS)
	}
	return f
}

// PullOptions configures the single upstream reader's command line.
type PullOptions struct {
	// Source is the upstream to download, read exactly as the served remux
	// reads its own (same reconnect policy, headers, container flags, pacing).
	Source NetworkSource

	// Reencode names the axes the spool container cannot carry as they are, decided
	// before the download starts (see carriage.Known). The zero value is the
	// ordinary all-copy pull.
	Reencode carriage.Axes

	// MaxHeight caps the height of the floor encode Reencode.Video asks for, and is
	// ignored by a copy, which carries no filter it could apply. It is the ceiling this
	// cast is already committed to (core.Resolve's MaxHeight): the served encode
	// downstream of this buffer scales to it anyway, so a floor encode that produced the
	// source's own 2160p would spend an encoder castor cannot afford on pixels the next
	// process throws away.
	MaxHeight media.HeightCap

	// Verbose selects -loglevel verbose (playlist/segment URLs, connection
	// lines) instead of the default warning level.
	Verbose bool

	// PCM additionally extracts mono s16le audio for the transcriber, on the second
	// extra pipe (see pcmFD): the first carries -progress, which every pull emits.
	PCM bool
	// PCMSampleRate is the audio sample rate for the PCM output.
	PCMSampleRate int
}

// ExtraPipes is how many extra output pipes this pull needs open before it can run:
// one for the -progress feed, plus one for the PCM tee when it is asked for. The
// builder answers it rather than the caller counting, because the builder is what
// routes the outputs, and a count that disagrees with the flags is not a missing feed
// but "Failed to open progress URL pipe:3: Bad file descriptor" before a byte is read.
func (opts PullOptions) ExtraPipes() int {
	if opts.PCM {
		return 2
	}
	return 1
}

// EncodeExtraPipes is how many extra output pipes an encode needs open: one, for the
// -progress feed EncodeArgs emits unconditionally.
const EncodeExtraPipes = 1

// PullArgs assembles the upstream download command line: a codec-copy remux
// of the source into append-only MPEG-TS on stdout, paced like a buffering
// player, with an optional PCM tee for transcription.
func PullArgs(opts PullOptions) []string {
	// Baseline "warning" (not "error") so HLS segment failures, "Failed to open
	// segment N" and "HTTP error 404 Not Found", reach the stderr ring tail.
	// They're warning-level in ffmpeg, so -loglevel error hides them, and a pull
	// whose every segment 404s (expired signed URL) then looks identical to a
	// silent stall: the playback gate reports "throttled or expired" with no
	// evidence. Capturing them lets the gate show the real reason without
	// --debug. Under --debug, verbose additionally streams the playlist/segment
	// URLs and connection lines so a stall can be reproduced by hand.
	//
	// It also must never drop to "error". The only signal castor has for the
	// MPEG-TS muxer's silent losses is a WARNING line ("Stream N, codec X, is muxed
	// as a private data stream and may not be recognized upon reading"), and
	// -fflags +discardcorrupt's packet drops are warnings too ("Packet corrupt
	// (stream = N, dts = ...), dropping it."). At -v error both are invisible while
	// the exit code stays 0.
	logLevel := "warning"
	if opts.Verbose {
		logLevel = "verbose"
	}

	// -progress on the runner's first extra pipe. Without it this process reported
	// nothing about itself at all: the only number a starved read produced was the
	// spool's byte count, and "spooled_bytes=14390648 rate_bytes_per_sec=0" printed
	// twice in a row cannot distinguish an origin that stopped from one that finished.
	// The feed carries ffmpeg's own speed=, which said 0.39x on that read (see
	// WatchProgress), and it costs one line every half second on a pipe the runner
	// drains anyway.
	//
	// The report period is spelled rather than left to ffmpeg's default, because the
	// playback gate's confidence window is counted in these blocks: it holds for a derived
	// number of them before a stated speed is allowed to convict a link, and it reads their
	// duration from the read policy that owns it (read.StatsPeriod) rather than from here.
	// A reader reporting half as often would otherwise halve that evidence silently.
	args := []string{"-nostats", "-loglevel", logLevel,
		"-progress", pipeURL(progressFD), "-stats_period", formatSeconds(read.StatsPeriod)}

	// The pull always paces, whatever the container: it buffers a whole title
	// into a spool the encoder tails, so running further ahead than the source's
	// own pace buys nothing and only spends the origin's patience.
	args = append(args, opts.Source.inputArgs(opts.Source.Read.Pace)...)

	// Output 1: codec-copy remux into the spool container on stdout (see
	// SpoolFormat for why it is what it is). The maps carry the same optional
	// suffix the encode uses, for the same reason.
	//
	// No explicit bitstream filter in either direction. Toward MPEG-TS ffmpeg
	// inserts the right *_mp4toannexb itself, per actual codec, so hardcoding the
	// h264 one would break HEVC sources. And the one
	// filter castor does own must never point this way: aac_adtstoasc into an
	// MPEG-TS output with an ADTS input exits 0 while the muxer rejects every packet
	// ("AAC bitstream not in ADTS format and extradata missing", repeated 188
	// times), leaving 8 to 61 of 189 to 470 packets and audio that does not decode.
	// It is structurally unreachable here, because the only producer of that filter
	// is an adaptation whose predicate requires a FramingOutOfBand destination and
	// this output is SpoolFormat, which the registry declares FramingInBand.
	//
	// What IS still open here is carriage, and the pull cannot close it by planning.
	// The MPEG-TS muxer never refuses a codec: FLAC, Vorbis, PCM, VP8, VP9, AV1,
	// MJPEG and msmpeg4v3 are all written as private data streams at exit 0, so the
	// spool lands with a track missing and the encode then dies mapping a stream
	// that is not there. Every other stage asks a copy adaptation table before
	// copying, which needs a probe; this one runs before any probe exists, on a URL
	// that may be single-use and that the read-once composition is built to reach within
	// milliseconds. So the pull is the one stage that adapts at runtime instead of
	// at plan time: it probes the spool it is in the middle of writing, compares it
	// to the source, and restarts itself with whatever axis went missing re-encoded
	// (see the pipeline's pull). That observes, it does not gate, and it needs no
	// codec allow-list to stay correct as ffmpeg changes. Reencode is how that
	// restart is expressed here.
	args = append(args, "-map", "0:v:0?", "-map", opts.Source.audioMap())
	// A stream copy on each axis, or the re-encode the spool container needs. The
	// targets are the floors core.DecideVideo and core.DecideAudio bottom out at,
	// which MPEG-TS carries by definition, so this cannot itself produce a spool the
	// container refuses.
	if opts.Reencode.Video {
		// The software baseline for the floor codec, named from the same registry the
		// decision layer selects from rather than spelled out again here.
		//
		// -crf under a VBV cap (capped CRF), never -crf alone. Quality-targeted encoding
		// with no ceiling is unbounded by construction, and this is the one reader that can
		// be asked for it on a source nobody chose the resolution of: at 3840x2160 a bare
		// -crf 23 asks a veryfast software encoder for tens of Mbit/s in realtime, which it
		// cannot hold, and a read that cannot hold realtime is a read the deliverability
		// judgement convicts (speed=0.0627 is what that looks like). So the recovery for a
		// copy that broke upstream would manufacture the undeliverable cast it exists to
		// escape. The cap is the budget the decision layer already gives H.264, read from
		// the one place both producers of the floor read it.
		floor, _ := softwareBaseline(media.FloorVideoCodec)
		args = append(args, "-c:v", floor.Name, "-preset", "veryfast", "-crf", "23",
			"-maxrate", media.FloorVideoMaxrate, "-bufsize", media.FloorVideoBufsize)
		// And capped in RESOLUTION, which is what the VBV cap above cannot do: the ceiling
		// bounds the bits, while what a veryfast software encoder cannot hold at 3840x2160 is
		// the pixel rate. Unscaled, this read measures under realtime on ordinary hardware, the
		// gate reports the SOURCE as too slow to watch, and the recovery that asked for this
		// encode (an axis a previous copy died on) manufactures the undeliverable verdict it
		// exists to escape. The picture is capped to the same height the encode downstream of
		// this buffer scales to, so nothing is lost that would have been kept.
		if f := scaleFilter(opts.MaxHeight); f != "" {
			args = append(args, "-vf", f)
		}
	} else {
		args = append(args, "-c:v", codecCopy)
	}
	if opts.Reencode.Audio {
		args = append(args, "-c:a", string(media.FloorAudioCodec),
			"-b:a", media.FloorAudioBitrate, "-ac", strconv.Itoa(media.FloorAudioChannels))
	} else {
		args = append(args, "-c:a", codecCopy)
	}
	args = append(args, "-f", SpoolFormat.Muxer, "pipe:1")

	if opts.PCM {
		// Output 2: mono PCM for whisper on the runner's second extra pipe, since the
		// first carries -progress. It is decoded rather than copied, so what the spool
		// container can carry has no bearing on it.
		args = append(args,
			"-map", opts.Source.audioMap(), "-vn",
			"-ac", "1",
			"-ar", strconv.Itoa(opts.PCMSampleRate),
			"-f", "s16le", pipeURL(pcmFD),
		)
	}
	return args
}

// drawtextFilter renders subtitle text bottom-centered with a translucent
// box, matching how dedicated subtitle renderers style their output.
// reload=1 makes ffmpeg re-open the textfile before every frame, which is
// what turns a static filter into a live subtitle track.
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

// escapeFilterArg escapes a value for passing through ffmpeg's two-level
// filter-string parser (graph parser, then per-filter option parser). Each
// level consumes one backslash, so a literal ':' in a filter option needs
// '\\:' in the input: one backslash survives the graph parser and the
// next is consumed by the option parser. Single-quote wrapping at graph
// level does NOT propagate to the option parser, so we don't rely on it.
func escapeFilterArg(s string) string {
	r := strings.NewReplacer(
		`\`, `\\\\`, // four backslashes in source → two in the arg → one survives both parsers
		`:`, `\\:`, // two backslashes + colon → one + colon after graph → literal colon after option parser
		`'`, `\\'`,
		`,`, `\\,`,
	)
	return r.Replace(s)
}
