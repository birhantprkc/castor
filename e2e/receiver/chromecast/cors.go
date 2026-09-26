package chromecast

import (
	"context"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
)

// CORS is whether the receiver holds a manifest's server to the cross-origin rules the Cast web player fetches it under.
type CORS string

const (
	// Ignored plays whatever LOAD names.
	Ignored CORS = "ignored"
	// Enforced refuses an HLS or DASH manifest whose server sends no Access-Control-Allow-Origin, as the Default Media Receiver does.
	Enforced CORS = "enforced"
)

const (
	// receiverOrigin is where the Default Media Receiver's page is served from, so every fetch of its player carries it.
	receiverOrigin = "https://www.gstatic.com"
	// preflightAgent differs from receiver.UserAgent, so judges counting the receiver's own fetches do not count this one.
	preflightAgent = "castor-e2e-receiver-preflight"
)

// adaptive are the manifests the web player reads by XHR; progressive media plays through a <video> element, outside CORS.
var adaptive = []string{"application/x-mpegURL", "application/vnd.apple.mpegurl", "application/dash+xml"}

// refuses reports whether the receiver fails a LOAD of content declared as contentType.
func (c CORS) refuses(ctx context.Context, content, contentType string) bool {
	declared, _, _ := mime.ParseMediaType(contentType)
	if c != Enforced || !slices.ContainsFunc(adaptive, func(t string) bool { return strings.EqualFold(t, declared) }) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, content, nil)
	if err != nil {
		return true
	}
	req.Header.Set("Origin", receiverOrigin)
	req.Header.Set("User-Agent", preflightAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return true
	}
	resp.Body.Close()
	allowed := resp.Header.Get("Access-Control-Allow-Origin")
	return allowed != "*" && allowed != receiverOrigin
}
