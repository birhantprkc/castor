// Castor is a proof of concept provided for lawful, personal, and educational
// use. This file is part of its stream-extraction pipeline and is intended only
// for accessing content you are authorized to view. Do not use it to infringe
// copyright or to circumvent access controls. The author does not endorse or
// condone piracy. See the "Purpose and disclaimer" section of the README.

package extract

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
)

var streamMIMETypes = map[string]bool{
	"audio/mpegurl":                 true,
	"audio/x-mpegurl":               true,
	"application/x-mpegurl":         true,
	"application/vnd.apple.mpegurl": true,
	"video/mp4":                     true,
	"video/webm":                    true,
	"video/x-matroska":              true,
}

// hlsURLPattern matches HTTP(S) URLs containing .m3u8 in console output.
var hlsURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+\.m3u8[^\s"'<>]*`)

type capturedStream struct {
	RawURL   string
	Headers  map[string]string
	MimeType string // confirmed by server; empty if only URL-pattern matched
}

type candidate struct {
	rawURL   string
	headers  map[string]string
	mimeType string
	score    int
}

// collector captures and deduplicates stream URLs from browser events.
type collector struct {
	ctx            context.Context
	patterns       []*regexp.Regexp
	maxCandidates  int
	mu             sync.Mutex
	candidates     []candidate
	requestHeaders map[network.RequestID]map[string]string // outgoing headers, keyed by request ID
	notify         chan struct{}                           // closed on first capture
}

func newCollector(ctx context.Context, patterns []*regexp.Regexp, maxCandidates int) *collector {
	return &collector{
		ctx:            ctx,
		patterns:       patterns,
		maxCandidates:  maxCandidates,
		requestHeaders: make(map[network.RequestID]map[string]string),
		notify:         make(chan struct{}),
	}
}

// Add records a URL if it matches capture patterns, is not a duplicate,
// and the candidate list is not full.
func (c *collector) Add(u string, headers map[string]string) {
	if !matchesPattern(u, c.patterns) {
		return
	}
	c.add(u, headers, "")
}

// AddByMIME records a URL when the server has confirmed the MIME type is a
// stream type. Pattern matching is skipped — the confirmed MIME takes precedence.
func (c *collector) AddByMIME(u string, mime string, headers map[string]string) {
	if !streamMIMETypes[strings.ToLower(mime)] {
		return
	}
	c.add(u, headers, strings.ToLower(mime))
}

// add deduplicates and appends a URL without pattern checking.
func (c *collector) add(u string, headers map[string]string, mimeType string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.candidates) >= c.maxCandidates {
		slog.DebugContext(c.ctx, "max candidates reached, skipping URL", "url", u)
		return
	}

	if i := slices.IndexFunc(c.candidates, func(cand candidate) bool { return cand.rawURL == u }); i >= 0 {
		// Already captured. A URL first seen in console output carries no request
		// headers; a later network sighting of the same URL does. Upgrade so
		// hotlink-protected hosts get the Referer/User-Agent they require instead
		// of a header-less 403.
		if len(c.candidates[i].headers) == 0 && len(headers) > 0 {
			c.candidates[i].headers = headers
			slog.DebugContext(c.ctx, "upgraded headers for captured URL", "url", u)
		} else {
			slog.DebugContext(c.ctx, "duplicate URL, skipping", "url", u)
		}
		return
	}

	slog.InfoContext(c.ctx, "captured stream", "url", u, "mime", mimeType)

	c.candidates = append(c.candidates, candidate{
		rawURL:   u,
		headers:  headers,
		mimeType: mimeType,
		score:    rankURL(u),
	})

	// Signal waiters on first capture.
	select {
	case <-c.notify:
	default:
		close(c.notify)
	}
}

// Entries returns captured streams sorted by score (descending).
func (c *collector) Entries() []capturedStream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return sortedEntries(c.candidates)
}

func (c *collector) HasHits() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.candidates) > 0
}

// hasMaster reports whether a master playlist has been captured. A master is
// the top of the HLS tree, so once one is seen there's nothing better to wait
// for and the collection window can be cut short.
func (c *collector) hasMaster() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(c.candidates, func(cand candidate) bool {
		return cand.score >= masterScore
	})
}

// Wait blocks until streams are found or the grace period expires.
// If streams are already captured, it waits collectionWindow for more, then returns.
// If no streams are found, it waits up to graceAfterActions before giving up.
func (c *collector) Wait(ctx context.Context, graceAfterActions, collectionWindow time.Duration) ([]capturedStream, error) {
	collectMore := func() []capturedStream {
		// Once a master playlist is in hand there's nothing better to wait for —
		// it already enumerates every variant — so return immediately instead of
		// burning the rest of collectionWindow. These source URLs are short-lived
		// signed links; every second spent here is a second of the token's life
		// gone before the puller can touch it. Without a master we keep collecting
		// (a late master or fallback variant may still arrive) up to the window.
		timer := time.NewTimer(collectionWindow)
		defer timer.Stop()
		poll := time.NewTicker(100 * time.Millisecond)
		defer poll.Stop()
		for {
			if c.hasMaster() {
				return c.Entries()
			}
			select {
			case <-timer.C:
				return c.Entries()
			case <-poll.C:
			case <-ctx.Done():
				return c.Entries()
			}
		}
	}

	if entries := c.Entries(); len(entries) > 0 {
		return collectMore(), nil
	}

	graceCtx, graceCancel := context.WithTimeout(ctx, graceAfterActions)
	defer graceCancel()

	select {
	case <-c.notify:
		return collectMore(), nil
	case <-graceCtx.Done():
		if entries := c.Entries(); len(entries) > 0 {
			return entries, nil
		}
		return nil, fmt.Errorf("no stream URL captured within grace period")
	}
}

// Listen returns an event handler for chromedp.ListenTarget that feeds
// network requests and console messages into the collector.
func (c *collector) Listen(ev any) {
	switch e := ev.(type) {
	case *network.EventRequestWillBeSent:
		// Only requestWillBeSent carries the real outgoing headers (Referer,
		// User-Agent, sec-ch-*). Keep them by request ID so a stream later
		// confirmed by MIME on responseReceived is fetched with the same headers.
		// Proxy and CDN hosts enforce hotlink protection and return 403 without
		// the browser's Referer, and responseReceived's own RequestHeaders come
		// back empty.
		headers := networkHeadersToMap(e.Request.Headers)
		c.storeHeaders(e.RequestID, headers)
		c.Add(e.Request.URL, headers)

	case *network.EventResponseReceived:
		c.AddByMIME(e.Response.URL, e.Response.MimeType, c.loadHeaders(e.RequestID))

	case *runtime.EventConsoleAPICalled:
		for _, arg := range e.Args {
			val := strings.Trim(string(arg.Value), `"`)
			for _, m := range hlsURLPattern.FindAllString(val, -1) {
				c.Add(m, nil)
			}
		}
	}
}

// storeHeaders records the outgoing request headers for a request ID.
func (c *collector) storeHeaders(id network.RequestID, headers map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requestHeaders[id] = headers
}

// loadHeaders returns the outgoing request headers recorded for a request ID,
// or nil if the request carried none.
func (c *collector) loadHeaders(id network.RequestID) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requestHeaders[id]
}

func networkHeadersToMap(h network.Headers) map[string]string {
	if len(h) == 0 {
		return nil
	}
	m := make(map[string]string, len(h))
	for k, v := range h {
		if s, ok := v.(string); ok {
			m[k] = s
		}
	}
	return m
}

// matchesPattern checks if a URL matches any of the capture patterns.
// Query parameters are stripped so encoded URLs in tracking pixels don't match.
func matchesPattern(u string, patterns []*regexp.Regexp) bool {
	stripped, _, _ := strings.Cut(u, "?")
	for _, re := range patterns {
		if re.MatchString(stripped) {
			return true
		}
	}
	return false
}

// variantPatterns are URL path substrings that indicate a variant/segment
// rather than a master playlist.
var variantPatterns = []string{
	"/720p/", "/1080p/", "/480p/", "/360p/", "/240p/",
	"/chunklist", "/media-", "/segment",
}

// masterScore is the rankURL bonus for a path containing "master". A candidate
// scoring at least this high is a master playlist: the variant penalty can't
// drag a non-master candidate up to it, so the threshold cleanly identifies one.
const masterScore = 100

// rankURL assigns a score to a captured URL for quality/variant selection.
// Higher score = more preferred (master playlists over variants).
func rankURL(rawURL string) int {
	score := 0

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return score
	}

	p := strings.ToLower(parsed.Path)

	if strings.Contains(p, "master") {
		score += masterScore
	}
	if strings.Contains(p, "playlist") {
		score += 50
	}

	if slices.ContainsFunc(variantPatterns, func(vp string) bool {
		return strings.Contains(p, vp)
	}) {
		score -= 50
	}

	return score
}

func sortedEntries(candidates []candidate) []capturedStream {
	sorted := slices.SortedFunc(slices.Values(candidates), func(a, b candidate) int {
		return cmp.Compare(b.score, a.score)
	})
	entries := make([]capturedStream, len(sorted))
	for i, c := range sorted {
		entries[i] = capturedStream{
			RawURL:   c.rawURL,
			Headers:  c.headers,
			MimeType: c.mimeType,
		}
	}
	return entries
}
