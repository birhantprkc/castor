// Package codec decides, track by track, what a cast copies and what it encodes, and into which container.
package codec

import (
	"context"
	"fmt"

	"github.com/stupside/castor/services/mediaserver/internal/cast/container"
	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// Encoders finds the host's working encoder for a codec.
type Encoders func(context.Context, media.Codec) (ffmpeg.Encoder, bool)

// Inputs is everything a plan is decided against.
type Inputs struct {
	// Caps is the connected device's advertised support.
	Caps     media.Capabilities
	Probe    media.ProbeInfo
	Measured bool
	// Into is the container this encode writes.
	Into      container.Format
	Decode    media.Axes
	MaxHeight media.HeightCap
	// Spliced is a source stitched from pieces encoded apart, or live and free to become so, which only a re-encode hands over as one stream.
	Spliced bool
	BurnIn  string
	// Encoders is the host's encoder lookup; required.
	Encoders Encoders
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
