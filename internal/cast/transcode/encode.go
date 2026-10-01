// Package transcode builds the ffmpeg command lines a cast runs: the read into its buffer, and the encode a renderer is served.
package transcode

import (
	"fmt"

	"github.com/stupside/castor/internal/cast/container"
	"github.com/stupside/castor/internal/cast/fetch"
	"github.com/stupside/castor/internal/cast/plan"
	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

// EncodeInput is what an encode reads: the zero value reads nothing and is refused.
type EncodeInput struct {
	pipe   container.FormatInfo
	pace   fetch.Pace
	source ProgramSource
}

// FromPipe reads the container fed to stdin, at pace.
func FromPipe(format container.FormatInfo, pace fetch.Pace) EncodeInput {
	return EncodeInput{pipe: format, pace: pace}
}

// FromSource reads the network source on the terms of its fetch plan.
func FromSource(source ProgramSource) EncodeInput { return EncodeInput{source: source} }

func (in EncodeInput) piped() bool { return in.pipe.Muxer != "" }

type EncodeOptions struct {
	Input  EncodeInput
	Probe  media.ProbeInfo      // Measurement copy decisions were made from.
	Format container.FormatInfo // Container to produce.
	Video  plan.VideoTrack      // Video axis of encode (zero = no decision, refused).
	Audio  plan.AudioTrack      // Audio axis of encode (zero = no decision, refused).
}

// Verbatim reports whether this encode would only re-mux the spool it reads into the same stream.
func (o EncodeOptions) Verbatim() bool {
	_, video := o.Video.Encode()
	_, audio := o.Audio.Encode()
	return o.Video.Decided() && o.Audio.Decided() && !video && !audio &&
		o.Input.pipe.Muxer == SpoolFormat.Muxer && o.Format.Muxer == SpoolFormat.Muxer &&
		o.Format.Delivery == container.DeliverStream
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
	args := paceArgs(in.pace, ffmpeg.Binary{})
	args = append(args, demuxFlags...)
	return append(args, "-f", in.pipe.Muxer, "-i", ffmpeg.StdinPipe)
}

// encodeMapArgs maps the first video and first audio track explicitly, and optionally.
func encodeMapArgs(in EncodeInput) []string {
	if in.piped() {
		return []string{"-map", "0:V:0?", "-map", "0:a:0?"}
	}
	return sourceMapArgs(in.source)
}

func encodeOutputTarget(opts EncodeOptions, tuning container.Tuning, burnIn string) []string {
	args := []string{"-progress", ffmpeg.ProgressPipe}
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

func EncodeArgs(opts EncodeOptions) (ffmpeg.Command, error) {
	if !opts.Input.piped() && len(opts.Input.source.program.Inputs) == 0 {
		return ffmpeg.Command{}, fmt.Errorf("encode has no input: build one with FromPipe or FromSource")
	}
	if err := decidedTracks(opts.Video, opts.Audio); err != nil {
		return ffmpeg.Command{}, err
	}
	tuning, err := encodeTuning(opts.Format)
	if err != nil {
		return ffmpeg.Command{}, err
	}
	codecs, output, err := trackArgs(opts.Probe, opts.Format, opts.Video, opts.Audio)
	if err != nil {
		return ffmpeg.Command{}, err
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
	return ffmpeg.NewCommand(append(args, encodeOutputTarget(opts, tuning, burnIn)...)), nil
}
