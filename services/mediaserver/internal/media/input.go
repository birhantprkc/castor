package media

import (
	"net/http"
	"net/url"
)

// InputID is the stable identity of a resource (survives filtering/reordering during plan build).
type InputID string

const (
	PrimaryInputID InputID = "primary"
	AudioInputID   InputID = "audio"
)

// Input is one independently fetched resource (fetch requirements from origins, not URLs).
type Input struct {
	ID  InputID
	URL *url.URL
	// Representation is the one representation of a manifest this input reads, empty for a URL naming its media alone.
	Representation       string
	Headers              http.Header
	ContentType          string
	RequiresRelaxedInput bool
	Fetch                Fetch
}

// Fetch is what castor knows about how a source must be fetched (from source.Origin, not URL).
type Fetch struct {
	// Segmented: many small files vs. one long read (deadline rules differ for playlists).
	Segmented bool

	// Framing: EXT-X-MAP decoder config (out-of-band fMP4 vs. in-band; unknown if unread).
	Framing Framing

	// Live: source has no end (arrives at 1x, cannot be outrun).
	Live bool

	// Spliced: pieces encoded apart, whose parameters and timestamps restart at each seam.
	Spliced bool
}

// seamed is a source whose timeline may break: stitched from pieces encoded apart, or live, where any reload can splice one in.
func (f Fetch) seamed() bool { return f.Spliced || f.Live }

// Fetching returns how this input must be fetched (content type proves segmented manifests).
func (i Input) Fetching() Fetch {
	fetch := i.Fetch
	fetch.Segmented = fetch.Segmented || IsSegmented(i.ContentType)
	return fetch
}
