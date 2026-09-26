package origin

import (
	"net/http"
	"path"
	"time"

	"github.com/stupside/castor/e2e/strategy"
)

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

// Quirk bends a resolved stream the way a real encoder or camera leaves it: it sets encode knobs and records facts.
type Quirk interface {
	strategy.Named
	Bend(s *Stream) error
}

// Packager publishes the encoded tracks: it writes the muxer half of the ffmpeg command.
type Packager interface {
	strategy.Named
	// Supports refuses a layout this packaging cannot publish, before anything is encoded.
	Supports(l Layout) error
	Package(dir string, l Layout) Output
}

// Behaviour is how the origin serves what it published: faithfully, as a live edge, or with hostility.
type Behaviour interface {
	strategy.Named
	Wrap(next http.Handler, p Published) http.Handler
}

// Edge marks a behaviour that serves the stream as a live edge, which a player joins wherever the edge is when it arrives.
type Edge interface{ LiveEdge() }

// Published is what a behaviour needs to know about the files it serves.
type Published struct {
	SegmentExt string
	// Since is when the origin started serving, the zero of any timeline.
	Since time.Time
	// Closing closes before the origin shuts down, releasing any request a behaviour holds.
	Closing <-chan struct{}
}

// Held blocks until the client leaves or the origin closes, whichever comes first.
func (p Published) Held(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-p.Closing:
	}
}

// IsSegment reports whether a request is for a media segment rather than a playlist or init segment.
func (p Published) IsSegment(r *http.Request) bool { return path.Ext(r.URL.Path) == p.SegmentExt }

// Carriage is how audio travels beside the video.
type Carriage string

const (
	// Muxed interleaves audio into every video rendition.
	Muxed Carriage = "muxed"
	// Separate publishes audio as its own rendition or adaptation set.
	Separate Carriage = "separate"
)

var carriages = []Carriage{Muxed, Separate}

// Layout is what a packager publishes: video renditions, and audio if any, as it travels.
type Layout struct {
	// Rungs is how many video renditions there are, tallest last.
	Rungs    int
	Audio    bool
	Carriage Carriage
	// SegmentExt names segments; empty for the packager's own.
	SegmentExt string
}

// Output is a packager's command half and what it wrote.
type Output struct {
	Entry string
	Args  []string
	// Then is each ffmpeg command that turns what Args wrote into what is published, run in order.
	Then [][]string
	// SegmentExt is the extension segments were written with; Types maps every extension written to its MIME.
	SegmentExt string
	Types      map[string]string
}
