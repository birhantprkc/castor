package origin

import "github.com/stupside/castor/e2e/strategy"

// VideoCodec is how a video track is encoded; its name is ffprobe's for the codec.
type VideoCodec interface {
	strategy.Named
	EncoderArgs() []string
}

// AudioCodec is how an audio track is encoded; its name is ffprobe's for the codec.
type AudioCodec interface {
	strategy.Named
	EncoderArgs() []string
}

// Transfer is the transfer characteristic a picture is tagged with; its name is how a case says it.
type Transfer interface {
	strategy.Named
	// Filter tags every frame, so any encoder writes the tag into its bitstream; empty tags nothing.
	Filter() string
}
