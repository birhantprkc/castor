package plan

import (
	"context"
	"fmt"

	"github.com/stupside/castor/internal/container"
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
	BurnIn    string
	// Encoders is the host's encoder lookup; required.
	Encoders Encoders
}

// PlanReason is evidence that made stream-copying one axis unsafe.
type PlanReason string

const (
	ReasonRendererVideo   PlanReason = "renderer-video-incompatible"
	ReasonRendererAudio   PlanReason = "renderer-audio-incompatible"
	ReasonContainerVideo  PlanReason = "container-video-incompatible"
	ReasonContainerAudio  PlanReason = "container-audio-incompatible"
	ReasonVideoCopyFailed PlanReason = "video-copy-failed"
	ReasonAudioCopyFailed PlanReason = "audio-copy-failed"
	ReasonHeightLimit     PlanReason = "height-limit"
	ReasonHDRPolicy       PlanReason = "hdr-policy"
	ReasonSubtitleBurnIn  PlanReason = "subtitle-burn-in"
)

// refusal is one row of an axis's copy-refusal table, with reason, prose, and when it applies.
type refusal struct {
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

func refuse(table []refusal, in Inputs) []Refusal {
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

// Reasons is the refusal vocabulary alone.
func (p MediaPlan) Reasons() []PlanReason {
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
