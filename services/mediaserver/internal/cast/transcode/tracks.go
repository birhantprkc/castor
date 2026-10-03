package transcode

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/stupside/castor/services/mediaserver/internal/cast/container"
	"github.com/stupside/castor/services/mediaserver/internal/cast/plan"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// No "-c copy" keyword; decided track name goes on -c:v and -c:a (nowhere else).

// axis is one decided track as the output carries it.
type axis struct {
	spec string // ffmpeg's stream specifier: "v" or "a"
	kind media.TrackKind
	name string // what -c names: "copy", or the encoder
	// codec is what lands in the container: the source's when copied, the encoder's otherwise.
	codec   media.Codec
	copying bool
	refused bool // the tables already say this container will not carry it
	// encode is the encoder's own options, nil when copied.
	encode []string
}

func videoAxis(probe media.ProbeInfo, video plan.Track[plan.VideoEncode], refused media.Axes) axis {
	a := axis{spec: "v", kind: media.TrackVideo, name: video.Name(), codec: probe.VideoCodec, copying: true, refused: refused.Video}
	if venc, ok := video.Encode(); ok {
		a.codec, a.copying, a.encode = venc.Encoder.Codec, false, videoEncodeArgs(venc)
	}
	return a
}

func audioAxis(probe media.ProbeInfo, audio plan.Track[plan.AudioEncode], refused media.Axes) axis {
	a := axis{spec: "a", kind: media.TrackAudio, name: audio.Name(), codec: probe.AudioCodec, copying: true, refused: refused.Audio}
	if aenc, ok := audio.Encode(); ok {
		a.codec, a.copying, a.encode = aenc.Codec, false, audioEncodeArgs(aenc)
	}
	return a
}

func decidedTracks(video plan.Track[plan.VideoEncode], audio plan.Track[plan.AudioEncode]) error {
	if !video.Decided() || !audio.Decided() {
		return fmt.Errorf("encode has an undecided axis (video decided: %t, audio decided: %t); a copy is a decision, not a default",
			video.Decided(), audio.Decided())
	}
	if venc, ok := video.Encode(); ok && (venc.Encoder.Name == "" || venc.Encoder.Codec == "") {
		return fmt.Errorf("video re-encode has no available encoder strategy")
	}
	return nil
}

func videoEncodeArgs(venc plan.VideoEncode) []string {
	args := slices.Clone(venc.Encoder.Flags)
	if venc.ToneMap {
		args = append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
	}
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

func hardwareInitArgs(video plan.Track[plan.VideoEncode]) []string {
	venc, ok := video.Encode()
	if !ok {
		return nil
	}
	return venc.Encoder.InitArgs
}

type axisArgs struct {
	args       []string
	movFlags   []string
	outputArgs []string
}

// args renders the axis into format; an encode takes its output's adaptations too (omitting them breaks E-AC-3).
func (a axis) args(format container.FormatInfo) (axisArgs, error) {
	cp := container.Adapt(a.kind, a.codec, format, a.copying)
	// A copy the tables already refuse must not be buildable.
	if a.copying && a.refused {
		return axisArgs{}, fmt.Errorf("copy of %q into %q on -c:%s is known not to be carriable; it should have been re-encoded",
			a.codec, format.ContentType, a.spec)
	}
	if !a.copying && len(cp.Filters) > 0 {
		return axisArgs{}, fmt.Errorf("re-encode to %q on -c:%s would carry bitstream filters %v; a repack belongs to a copy, so that row needs the copying predicate",
			a.codec, a.spec, cp.Filters)
	}
	out := axisArgs{args: []string{"-c:" + a.spec, a.name}, movFlags: cp.MovFlags, outputArgs: cp.OutputArgs}
	// ffmpeg takes the whole chain as one comma separated value.
	if len(cp.Filters) > 0 {
		out.args = append(out.args, "-bsf:"+a.spec, strings.Join(cp.Filters, ","))
	}
	out.args = append(out.args, a.encode...)
	return out, nil
}

func audioEncodeArgs(aenc plan.AudioEncode) []string {
	var args []string
	if aenc.Resync {
		// The filter graph rebuilds at a seam and replays timestamps; each packet must follow the one before.
		args = append(args, "-bsf:a", "setts=pts=max(PTS\\,PREV_OUTPTS+DURATION):dts=max(DTS\\,PREV_OUTDTS+DURATION)")
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
	return args
}

// trackArgs renders both decided axes into format: codecs first, then the output options their adaptations add.
func trackArgs(probe media.ProbeInfo, format container.FormatInfo, video plan.Track[plan.VideoEncode], audio plan.Track[plan.AudioEncode]) (codecs, output []string, err error) {
	refused := container.Known(probe, format)
	v, err := videoAxis(probe, video, refused).args(format)
	if err != nil {
		return nil, nil, err
	}
	a, err := audioAxis(probe, audio, refused).args(format)
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
