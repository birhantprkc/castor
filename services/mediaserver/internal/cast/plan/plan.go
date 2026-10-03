// Package plan decides, track by track, what a cast copies and what it encodes, and into which container.
package plan

import (
	"context"
	"fmt"

	"github.com/stupside/castor/services/mediaserver/internal/cast/container"
	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

type Encoders func(context.Context, media.Codec) (ffmpeg.Encoder, bool)

type Inputs struct {
	// Caps is the connected device's advertised support.
	Caps     media.Capabilities
	Probe    media.ProbeInfo
	Measured bool
	// Into is the container this encode writes.
	Into      container.FormatInfo
	Decode    media.Axes
	MaxHeight media.HeightCap
	// Spliced is a source stitched from pieces encoded apart, or live and free to become so, which only a re-encode hands over as one stream.
	Spliced bool
	BurnIn  string
	// Encoders is the host's encoder lookup; required.
	Encoders Encoders
}

// Reason is evidence that made stream-copying one axis unsafe.
type Reason string

const (
	reasonDeviceVideo     Reason = "device-video-incompatible"
	reasonDeviceAudio     Reason = "device-audio-incompatible"
	reasonContainerVideo  Reason = "container-video-incompatible"
	reasonContainerAudio  Reason = "container-audio-incompatible"
	reasonVideoCopyFailed Reason = "video-copy-failed"
	reasonAudioCopyFailed Reason = "audio-copy-failed"
	reasonHeightLimit     Reason = "height-limit"
	reasonHDRPolicy       Reason = "hdr-policy"
	reasonInterlaced      Reason = "interlaced"
	reasonRotated         Reason = "rotated"
	reasonSampleRate      Reason = "sample-rate"
	reasonSpliced         Reason = "spliced"
	reasonSubtitleBurnIn  Reason = "subtitle-burn-in"
)

// refusalRule is one row of an axis's copy-refusal table, with reason, prose, and when it applies.
type refusalRule struct {
	reason Reason
	// why is the same refusal in prose for the one line that states it, and is a function of the subject.
	why func(Inputs) string
	// when reports whether this row refuses this subject.
	when func(Inputs) bool
}

// says returns a constant why function for a row whose reasoning is the same sentence for every subject.
func says(why string) func(Inputs) string { return func(Inputs) string { return why } }

// Refusal is one fired row: why this axis is not copied.
type Refusal struct {
	Reason Reason
	Why    string
}

func refuse(table []refusalRule, in Inputs) []Refusal {
	var fired []Refusal
	for _, r := range table {
		if r.when(in) {
			fired = append(fired, Refusal{Reason: r.reason, Why: r.why(in)})
		}
	}
	return fired
}

// Plan is the complete codec plan for an FFmpeg encode; Refusals explain why it is not a copy.
type Plan struct {
	Video    Track[VideoEncode]
	Audio    Track[AudioEncode]
	Refusals []Refusal
}

// Encoded returns which halves of the program this plan produces rather than passes through.
func (p Plan) Encoded() media.Axes {
	_, video := p.Video.Encode()
	_, audio := p.Audio.Encode()
	return media.Axes{Video: video, Audio: audio}
}

// Served plans the encode a device is served, from its capabilities, track by track.
func Served(ctx context.Context, in Inputs) (Plan, error) {
	video, videoRefusals, err := decideVideo(ctx, in)
	if err != nil {
		return Plan{}, fmt.Errorf("planning video: %w", err)
	}
	audio, audioRefusals, err := decideAudio(in)
	if err != nil {
		return Plan{}, fmt.Errorf("planning audio: %w", err)
	}
	return Plan{Video: video, Audio: audio, Refusals: append(videoRefusals, audioRefusals...)}, nil
}
