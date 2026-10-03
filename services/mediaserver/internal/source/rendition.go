package source

import (
	"net/url"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// Rendition is one version of a program a source offered, as the source described it.
type Rendition struct {
	URL *url.URL

	// Representation names the rung inside a manifest that publishes its ladder behind one URL.
	Representation string

	// AudioURL is the companion audio rendition for this rung.
	AudioURL *url.URL

	// Bitrate is the rate the source declared, 0 when it declared none.
	Bitrate media.Bitrate

	// Height is the declared display height, 0 when the source omitted it.
	Height int

	// Declared is the codec envelope the source declared for this rung.
	Declared *media.ProbeInfo
}
