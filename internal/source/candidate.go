package source

import (
	"net/http"
	"net/url"
	"time"

	"github.com/stupside/castor/internal/media"
)

type Candidate struct {
	URL *url.URL

	Ladder Ladder

	Probe *media.ProbeInfo

	// LastResort is a link ranking admitted without a measurement to back it.
	LastResort bool

	Headers     http.Header
	ContentType string
}

// Bitrate is the rate a measurement established for this candidate, 0 when none did.
func (c *Candidate) Bitrate() media.Bitrate {
	if c.Probe == nil {
		return 0
	}
	return media.Bitrate(c.Probe.BitRate)
}

// NormalizeStreamHeaders returns a copy of browser-captured headers ready to replay to the puller.
func NormalizeStreamHeaders(h http.Header) http.Header {
	out := h.Clone()
	if out == nil {
		return nil
	}
	out.Del("Range")
	out.Del("Accept-Encoding")
	if out.Get("Origin") == "" {
		if origin := originOf(out.Get("Referer")); origin != "" {
			out.Set("Origin", origin)
		}
	}
	return out
}

// originOf returns the scheme://host origin of an absolute URL, or "" if s is not one.
func originOf(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// minContentDuration is the shortest runtime treated as real content; pre-roll ads run well under it.
const minContentDuration = 5 * time.Minute

// ShorterThanContent reports a known runtime under a feature's, which is what an ad runs.
func ShorterThanContent(runtime time.Duration) bool {
	return runtime > 0 && runtime < minContentDuration
}
