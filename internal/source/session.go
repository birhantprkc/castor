package source

import (
	"net/http"
	"slices"
	"strings"
)

// WithSession adds the origin's session to a read's headers, the page's own cookie winning where both name one.
func WithSession(headers, session http.Header) http.Header {
	jarred, _ := http.ParseCookie(session.Get("Cookie"))
	if len(jarred) == 0 {
		return headers
	}
	page, _ := http.ParseCookie(headers.Get("Cookie"))
	merged := slices.Concat(page, slices.DeleteFunc(jarred, func(j *http.Cookie) bool {
		return slices.ContainsFunc(page, func(p *http.Cookie) bool { return p.Name == j.Name })
	}))
	out := headers.Clone()
	if out == nil {
		out = http.Header{}
	}
	out.Set("Cookie", CookieHeader(merged))
	return out
}

// CookieHeader is the Cookie request header that carries cookies.
func CookieHeader(cookies []*http.Cookie) string {
	pairs := make([]string, len(cookies))
	for i, c := range cookies {
		pairs[i] = c.Name + "=" + c.Value
	}
	return strings.Join(pairs, "; ")
}
