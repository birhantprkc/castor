package extract

import (
	"net/http"
	"net/url"
)

// replayable is a copy of headers the browser sent, ready for another reader to send.
func replayable(h http.Header) http.Header {
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
