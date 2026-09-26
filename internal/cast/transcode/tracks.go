package transcode

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/stupside/castor/internal/cast/container"
	"github.com/stupside/castor/internal/cast/plan"
	"github.com/stupside/castor/internal/media"
)

// No "-c copy" keyword; decided track name goes on -c:v and -c:a (nowhere else).

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

func hardwareInitArgs(video plan.VideoTrack) []string {
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
