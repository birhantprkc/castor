// Package ffprobe binds the source layer's measurements to the ffprobe binary.
// It is the only place under internal/source that starts a subprocess.
//
// It is a package of its own because measurement being welded to the policy that
// reads it is what left that policy untested: probing was an unexported exec call
// at function granularity, so the rule that admits a candidate castor has proved
// it cannot open had no test at all, and the tests that did exist skipped whole
// files whenever ffprobe was absent from PATH. Behind a port the rules are
// exercised over a fake and the subprocess is exercised here, against a real
// origin, where it is the thing under test rather than a prerequisite.
package ffprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stupside/castor/internal/media"
)

// Prober answers what a source is by asking ffprobe. It satisfies
// resolve.Measurer.
//
// The binary path and the budget are fixed for the life of the process while the
// subject changes per call, so they are held here instead of being threaded
// through every measurement the layer above makes.
type Prober struct {
	path    string
	timeout time.Duration
}

// New binds a prober to the ffprobe binary and the budget one measurement gets.
func New(ffprobePath string, timeout time.Duration) *Prober {
	return &Prober{path: ffprobePath, timeout: timeout}
}

// Measure runs ffprobe and reports what the source carries: its container, its
// duration, its bit rate and whether there is a castable program in it.
//
// The media.Reach travels out alongside, on every path including the failing ones,
// because the caller's decision turns on it: a measurement that failed because the
// origin refused the link and one that failed because castor ran out of budget are
// the same error and opposite facts (see media.Reach). It is reported even when the
// error came after a successful read (unparseable JSON, no format name), since the
// origin did serve the source in that case and only the answer was unusable.
func (p *Prober) Measure(ctx context.Context, s *media.Stream) (*media.StreamInfo, media.Reach, error) {
	args := []string{
		// Suppress non-error output so only JSON is written to stdout.
		// Use "error" (not "quiet") so stderr captures failure details.
		"-v", "error",
		// Output as JSON for structured parsing
		"-print_format", "json",
		// Format name + bit rate identify the container and rank quality;
		// duration separates a feature title from a spliced-in pre-roll ad;
		// the per-stream codec/type/dimensions let us reject decoy playlists
		// (image-only "video", no audio) that would crash the puller's
		// stream mapping.
		"-show_entries", "format=format_name,bit_rate,duration:stream=codec_type,codec_name,width,height",
	}

	// Forward any HTTP headers (e.g. Referer, User-Agent) to the stream server
	args = append(args, media.HeaderArgs(s.Headers)...)
	args = append(args, media.HLSInputArgs...)
	args = append(args, s.URL.String())

	slog.DebugContext(ctx, "running ffprobe", "url", s.URL.String(), "header_count", len(s.Headers))

	out, reach, err := p.run(ctx, args...)
	if err != nil {
		return nil, reach, err
	}

	var result struct {
		Streams []struct {
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			CodecName string `json:"codec_name"`
			CodecType string `json:"codec_type"`
		} `json:"streams"`
		Format struct {
			BitRate    string `json:"bit_rate"`
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, reach, fmt.Errorf("parsing ffprobe output: %w", err)
	}

	if result.Format.FormatName == "" {
		return nil, reach, fmt.Errorf("ffprobe returned no format name")
	}

	// A container castor has no name for is not a failure: the answer is only used
	// to choose input flags and to decide whether the renderer might be handed the
	// URL, and an empty content type answers both of those the safe way. Aborting
	// here meant a raw MPEG-TS stream could not be cast at all, even though castor
	// muxes MPEG-TS itself.
	contentType := media.FormatToContentType(result.Format.FormatName)
	if contentType == "" {
		slog.DebugContext(ctx, "unrecognised container; the source will be read rather than handed over",
			"format_name", result.Format.FormatName)
	}

	var bitRate int64
	if result.Format.BitRate != "" {
		var err error
		bitRate, err = strconv.ParseInt(result.Format.BitRate, 10, 64)
		if err != nil {
			slog.WarnContext(ctx, "ffprobe returned non-numeric bit_rate, defaulting to 0", "bit_rate", result.Format.BitRate)
		}
	}

	info := &media.StreamInfo{BitRate: bitRate, ContentType: contentType}

	// Fractional seconds ("5405.400000"), or absent/"N/A" for live streams.
	// Unparseable stays zero, which callers read as "unknown", not "short".
	if secs, err := strconv.ParseFloat(result.Format.Duration, 64); err == nil && secs > 0 {
		info.Duration = time.Duration(secs * float64(time.Second))
	}

	for _, s := range result.Streams {
		switch s.CodecType {
		case "video":
			// A real video track has dimensions and a non-image codec. Decoy
			// playlists carry a single png/mjpeg "video" with no size.
			if s.Width > 0 && s.Height > 0 && !isImageCodec(s.CodecName) {
				info.HasVideo = true
				if info.VideoHeight == 0 {
					info.VideoHeight = s.Height
				}
			}
		case "audio":
			info.HasAudio = true
		}
	}
	return info, reach, nil
}

// OpensUnaided reports whether ffprobe can open the source using its own default
// checks, i.e. without the relaxations castor applies to every HLS input
// (media.HLSInputArgs). It is the one honest test of whether a renderer that
// fetches the URL for itself will get anything: those flags exist to accept
// playlists a conforming reader rejects, so a source that needs them is refused
// by every reader that does not know to relax, a Cast receiver included.
//
// A source that is simply unreachable also reports false. That is the safe way
// round: the cast is served instead of handed over, and an unreachable source
// fails at the pull either way.
func (p *Prober) OpensUnaided(ctx context.Context, s *media.Stream) bool {
	// Whether it opens is the whole answer, so ask for the cheapest field there
	// is. media.HLSInputArgs is deliberately absent: those relaxations are what
	// this probe exists to do without, and adding them back makes it answer yes
	// for every source.
	args := []string{"-v", "error", "-show_entries", "format=format_name", "-of", "csv=p=0"}
	args = append(args, media.HeaderArgs(s.Headers)...)
	args = append(args, s.URL.String())

	// The reach is deliberately dropped: whether the source opens is the whole
	// answer here, and an unreachable source answering false is the safe way round
	// (the cast is served instead of handed over, and an unreachable source fails at
	// the pull either way). Nothing downstream of this bool can act on why.
	if _, _, err := p.run(ctx, args...); err != nil {
		slog.DebugContext(ctx, "source does not open under default reader checks", "url", s.URL.String(), "error", err)
		return false
	}
	return true
}

// run runs ffprobe under a bounded timeout and returns its stdout, plus how far the
// origin let it get. ffprobe writes the reason it failed to stderr, so a failure
// carries that text into the error instead of leaving the caller with a bare exit
// status, and that same text is the only material there is for the reach.
func (p *Prober) run(ctx context.Context, args ...string) ([]byte, media.Reach, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, p.path, args...).Output()
	if err == nil {
		return out, media.ReachOpened, nil
	}

	// The context is checked before the exit status because a probe castor killed
	// establishes nothing about the origin, and CommandContext reports that kill as
	// a signal exit with empty stderr, which on the status alone is
	// indistinguishable from ffprobe crashing. The budget is named in the error
	// because it is the number an operator would have to change.
	if ctx.Err() != nil {
		return nil, media.ReachUnproven, fmt.Errorf("ffprobe: %w (measurement budget %s)", err, p.timeout)
	}

	var e *exec.ExitError
	if errors.As(err, &e) && len(e.Stderr) > 0 {
		return nil, classifyReach(string(e.Stderr)), fmt.Errorf("ffprobe: %w\n%s", err, e.Stderr)
	}
	return nil, media.ReachUnproven, fmt.Errorf("ffprobe: %w", err)
}

// refusals are the origin answers no reader can talk its way past, matched on the
// text ffmpeg's HTTP protocol writes for them ("Server returned 403 Forbidden
// (access denied)"). The status is the only discriminator available: ffprobe exits
// 1 for every HTTP failure alike, so a spent signed link and a throttled one are
// one exit code.
//
// 429 and the 5xx statuses are deliberately absent. A rate limiter answering a
// burst of probes castor itself fired, or an origin having a bad minute, says
// nothing about whether the puller can read the link, and treating either as a
// refusal is how one unlucky moment convicts every candidate behind a signature.
var refusals = []string{"401 Unauthorized", "403 Forbidden", "404 Not Found", "410 Gone"}

// classifyReach reads how far the origin let ffprobe get out of what ffprobe said
// on the way out. Anything unrecognised is ReachUnproven, which is the lenient
// answer: this is prose matching, so it may only narrow a verdict the caller would
// otherwise reach leniently, never enable a harsher one.
func classifyReach(stderr string) media.Reach {
	if slices.ContainsFunc(refusals, func(status string) bool { return strings.Contains(stderr, status) }) {
		return media.ReachRefused
	}
	return media.ReachUnproven
}

// imageCodecs are ffmpeg codec names that decode to a still image rather than
// motion video. A playlist whose only "video" track is one of these is a decoy.
var imageCodecs = map[string]bool{
	"png": true, "apng": true, "mjpeg": true, "jpeg": true, "jpegls": true,
	"bmp": true, "gif": true, "tiff": true, "webp": true, "ppm": true,
}

func isImageCodec(name string) bool { return imageCodecs[name] }
