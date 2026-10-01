// Package plan decides, track by track, what a cast copies and what it encodes, and into which container.
package plan

import (
	"context"
	"fmt"

	"github.com/stupside/castor/internal/cast/container"
	"github.com/stupside/castor/internal/media"
)

type Encoders func(context.Context, media.Codec) (Encoder, bool)

type Inputs struct {
	// Caps is the connected renderer's advertised support.
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

// PlanReason is evidence that made stream-copying one axis unsafe.
type PlanReason string

const (
	reasonRendererVideo   PlanReason = "renderer-video-incompatible"
	reasonRendererAudio   PlanReason = "renderer-audio-incompatible"
	reasonContainerVideo  PlanReason = "container-video-incompatible"
	reasonContainerAudio  PlanReason = "container-audio-incompatible"
	reasonVideoCopyFailed PlanReason = "video-copy-failed"
	reasonAudioCopyFailed PlanReason = "audio-copy-failed"
	reasonHeightLimit     PlanReason = "height-limit"
	reasonHDRPolicy       PlanReason = "hdr-policy"
	reasonInterlaced      PlanReason = "interlaced"
	reasonRotated         PlanReason = "rotated"
	reasonSampleRate      PlanReason = "sample-rate"
	reasonSpliced         PlanReason = "spliced"
	reasonSubtitleBurnIn  PlanReason = "subtitle-burn-in"
)

// refusalRule is one row of an axis's copy-refusal table, with reason, prose, and when it applies.
type refusalRule struct {
	reason PlanReason
	// why is the same refusal in prose for the one line that states it, and is a function of the subject.
	why func(Inputs) string
	// when reports whether this row refuses this subject.
	when func(Inputs) bool
}

// says returns a constant why function for a row whose reasoning is the same sentence for every subject.
func says(why string) func(Inputs) string { return func(Inputs) string { return why } }

// Refusal is one fired row: why this axis is not copied.
type Refusal struct {
	Reason PlanReason
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

// MediaPlan is the complete codec plan for an FFmpeg encode; Refusals explain why it is not a copy.
type MediaPlan struct {
	Video    VideoTrack
	Audio    AudioTrack
	Refusals []Refusal
}

// reasons is the refusal vocabulary alone.
func (p MediaPlan) reasons() []PlanReason {
	reasons := make([]PlanReason, len(p.Refusals))
	for i, r := range p.Refusals {
		reasons[i] = r.Reason
	}
	return reasons
}

// Encoded returns which halves of the program this plan produces rather than passes through.
func (p MediaPlan) Encoded() media.Axes {
	_, video := p.Video.Encode()
	_, audio := p.Audio.Encode()
	return media.Axes{Video: video, Audio: audio}
}

// PlanMedia builds one capability-driven plan for both mapped tracks.
func PlanMedia(ctx context.Context, in Inputs) (MediaPlan, error) {
	video, videoRefusals, err := decideVideo(ctx, in)
	if err != nil {
		return MediaPlan{}, fmt.Errorf("planning video: %w", err)
	}
	audio, audioRefusals, err := decideAudio(in)
	if err != nil {
		return MediaPlan{}, fmt.Errorf("planning audio: %w", err)
	}
	return MediaPlan{Video: video, Audio: audio, Refusals: append(videoRefusals, audioRefusals...)}, nil
}
