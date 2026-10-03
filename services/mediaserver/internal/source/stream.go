package source

import (
	"net/http"
	"net/url"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

type Stream struct {
	URL *url.URL

	Ladder Ladder

	Probe *media.ProbeInfo

	// LastResort is a link ranking admitted without a measurement to back it.
	LastResort bool

	Headers     http.Header
	ContentType string
}

// Bitrate is the rate a measurement established for this stream, 0 when none did.
func (c *Stream) Bitrate() media.Bitrate {
	if c.Probe == nil {
		return 0
	}
	return media.Bitrate(c.Probe.BitRate)
}

// minContentDuration is the shortest runtime treated as real content; pre-roll ads run well under it.
const minContentDuration = 5 * time.Minute

// ShorterThanContent reports a known runtime under a feature's, which is what an ad runs.
func ShorterThanContent(runtime time.Duration) bool {
	return runtime > 0 && runtime < minContentDuration
}
