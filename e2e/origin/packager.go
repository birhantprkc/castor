package origin

import "github.com/stupside/castor/e2e/strategy"

// Packager publishes the encoded tracks: it writes the muxer half of the ffmpeg command.
type Packager interface {
	strategy.Named
	// Supports refuses a layout this packaging cannot publish, before anything is encoded.
	Supports(l Layout) error
	Package(dir string, l Layout) Output
	// Muxes is the ffmpeg muxer the media is written in, as MPEGTS.
	Muxes() string
}

// MPEGTS is the muxer with no display matrix, whose 33-bit timestamps wrap.
const MPEGTS = "mpegts"

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
	// Files are written into the directory, by name, before anything is encoded.
	Files map[string][]byte
	// SegmentExt is the extension segments were written with; Types maps every extension written to its MIME.
	SegmentExt string
	Types      map[string]string
}
