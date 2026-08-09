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
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/stupside/castor/internal/media"
)

// hlsURLPattern matches HTTP(S) URLs containing .m3u8 in console output.
var hlsURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+\.m3u8[^\s"'<>]*`)

type capturedStream struct {
	RawURL   string
	Headers  http.Header
	MimeType string // confirmed by server; empty if only URL-pattern matched
	// Ladder is what the captured document's own tags said about renditions, or
	// LadderUnknown when the body could not be read (see askForLadder).
	Ladder media.Ladder
}

type candidate struct {
	score    int
	reqID    network.RequestID
	rawURL   string
	mimeType string
	ladder   media.Ladder
}

// hasLadder reports the one thing about a capture worth stopping the page for and
// worth putting ahead of a better-named URL: this document advertises renditions, so
// it carries rungs a cast can move between.
//
// Unknown answers false and shares that answer with a confirmed single rendition,
// which is the lenient reading media.LadderUnknown requires: a body Chrome would not
// hand over must not push its candidate anywhere.
func (cand candidate) hasLadder() bool { return cand.ladder == media.LadderMultivariant }

// isPlaylist reports whether this capture is an HLS document, which is the only
// container whose grammar can answer the ladder question at all: everything else
// castor captures is one program by construction.
func (cand candidate) isPlaylist() bool {
	u, err := url.Parse(cand.rawURL)
	if err != nil {
		return false
	}
	return contentTypeOf(u, cand.mimeType) == media.HLS
}

// contentTypeOf names what a capture carries, preferring the URL's extension and
// falling back to the MIME the server confirmed. Both are pure lookups, so cmp.Or's
// eager evaluation costs nothing. Empty means neither recognised it.
func contentTypeOf(u *url.URL, mime string) string {
	return cmp.Or(media.DetectFromExtension(u), media.DetectFromMIME(mime))
}

// collector captures and deduplicates stream URLs from browser events.
type collector struct {
	ctx            context.Context
	patterns       []*regexp.Regexp
	maxCandidates  int
	mu             sync.Mutex
	candidates     []candidate
	requestHeaders map[network.RequestID]http.Header
	// bodyRead records the requests a body read has already been fired for, so a
	// duplicated or retried loadingFinished cannot pull the same document twice.
	bodyRead map[network.RequestID]struct{}
	notify   chan struct{} // closed on first capture
}

func newCollector(ctx context.Context, patterns []*regexp.Regexp, maxCandidates int) *collector {
	return &collector{
		ctx:            ctx,
		patterns:       patterns,
		maxCandidates:  maxCandidates,
		requestHeaders: make(map[network.RequestID]http.Header),
		bodyRead:       make(map[network.RequestID]struct{}),
		notify:         make(chan struct{}),
	}
}

// addByPattern records a URL if it matches capture patterns.
func (c *collector) addByPattern(u string, reqID network.RequestID) {
	if !matchesPattern(u, c.patterns) {
		return
	}
	c.add(u, reqID, "")
}

// addByMIME records a URL when the server has confirmed the MIME type is a
// stream type. Pattern matching is skipped: the confirmed MIME takes precedence.
func (c *collector) addByMIME(u string, reqID network.RequestID, mime string) {
	if media.DetectFromMIME(mime) == "" {
		return
	}
	c.add(u, reqID, strings.ToLower(mime))
}

// add deduplicates and appends a URL. On a duplicate it enriches the existing
// candidate in place rather than adding a second row.
func (c *collector) add(u string, reqID network.RequestID, mimeType string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if i := slices.IndexFunc(c.candidates, func(cand candidate) bool { return cand.rawURL == u }); i >= 0 {
		// Already captured. A console-first URL has no request ID (so no
		// headers); adopting a later network sighting's ID gives hotlink-
		// protected hosts the Referer/Origin they need instead of a 403. Fill in
		// a MIME confirmed later the same way.
		if c.candidates[i].reqID == "" && reqID != "" {
			c.candidates[i].reqID = reqID
			slog.DebugContext(c.ctx, "attached request headers to captured URL", "url", u)
		}
		if c.candidates[i].mimeType == "" && mimeType != "" {
			c.candidates[i].mimeType = mimeType
		}
		return
	}

	if len(c.candidates) >= c.maxCandidates {
		slog.DebugContext(c.ctx, "max candidates reached, skipping URL", "url", u)
		return
	}

	slog.InfoContext(c.ctx, "captured stream", "url", u, "mime", mimeType)

	c.candidates = append(c.candidates, candidate{
		rawURL:   u,
		reqID:    reqID,
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

// Entries returns captured streams best first, each with its outgoing headers
// resolved from the merged per-request header set.
//
// A document confirmed to advertise renditions leads, and only then the URL score.
// That order is load-bearing rather than cosmetic: the ranker keeps at most a handful
// of candidates per host and drops the rest unmeasured, precisely because an embed
// proxy publishes a master plus a long tail of variant playlists behind one signature.
// A master ordered by its path alone sits somewhere in that tail, and a master dropped
// before it is measured takes its whole ladder with it.
func (c *collector) Entries() []capturedStream {
	c.mu.Lock()
	defer c.mu.Unlock()

	sorted := slices.SortedFunc(slices.Values(c.candidates), func(a, b candidate) int {
		if a.hasLadder() != b.hasLadder() {
			if a.hasLadder() {
				return -1
			}
			return 1
		}
		return cmp.Compare(b.score, a.score)
	})
	entries := make([]capturedStream, len(sorted))
	for i, cand := range sorted {
		entries[i] = capturedStream{
			RawURL:   cand.rawURL,
			Headers:  c.requestHeaders[cand.reqID], // nil reqID → nil headers (console-only)
			MimeType: cand.mimeType,
			Ladder:   cand.ladder,
		}
	}
	return entries
}

// HasHits reports whether anything at all was captured. Its one caller asks it in
// place of hasMaster because it faces a different alternative: a failed navigation is
// about to discard the session entirely, so any candidate is better than none. Nothing
// that can keep working toward a master should stop on this.
func (c *collector) HasHits() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.candidates) > 0
}

// hasMaster reports whether a document advertising renditions has been captured. A
// master is the top of the HLS tree: it enumerates every variant, so once one is in
// hand there is nothing better to wait for.
//
// This is the single definition of a capture worth stopping for, and both places that
// stop early read it: the action pipeline (runActions) and the collection window
// (Wait). They used to disagree, the pipeline stopping on any hit at all and the
// window only on a master, which let one early chunklist capture end the pipeline
// before the page had been driven far enough to request the master and left the ranker
// a single rendition with no ladder behind it.
//
// It reads what the captured document's own tags said (see askForLadder), not the
// URL's shape. A master is routinely served as index.m3u8, so a path-substring test
// answers no for exactly the shape that matters: three runs in a row ended with
// renditions=1 sole=true, holding a document castor had already downloaded.
//
// Unknown is not a master, and the cost of that is bounded on purpose. A body Chrome
// would not hand over means the window runs its full course and the action list is
// exhausted, which is what a page publishing no master gets anyway: seconds of a
// signed link's life. Reading unknown as a master would instead end the collection on
// a chunklist, which is the failure this whole test exists to prevent.
func (c *collector) hasMaster() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(c.candidates, candidate.hasLadder)
}

// HLS tag names, matched literally because that is how they are defined (RFC 8216) and
// how every player reads them. Lower-casing a whole body to be generous would also
// match the same text inside a URL, a title or a comment, and a false master is worse
// than an unknown one: it ends the collection window early on a document with no
// ladder in it.
const (
	playlistTag     = "#EXTM3U"
	multivariantTag = "#EXT-X-STREAM-INF"
)

// ladderOf reads a captured body as an HLS document and reports what its grammar says
// about renditions. This is the whole definition of a master: an #EXT-X-STREAM-INF tag
// IS the declaration of a variant, so a document carrying one offers a ladder and a
// document carrying none is itself a rendition.
//
// A body with no #EXTM3U in it is not a playlist, whatever it was requested as: a CDN
// error page, an HTML interstitial and an empty response all read that way, and none
// of them is evidence that the source published a single rendition. Those answer
// unknown, which is the lenient value (see media.LadderUnknown).
func ladderOf(body string) media.Ladder {
	if !strings.Contains(body, playlistTag) {
		return media.LadderUnknown
	}
	if strings.Contains(body, multivariantTag) {
		return media.LadderMultivariant
	}
	return media.LadderSole
}

// askForLadder reads the response body Chrome has already downloaded for one finished
// request and records what the document's own tags say about renditions.
//
// Reading the body is the only honest way to tell a master from a media playlist here.
// Nothing about a URL says which one it is, and deriving a master's URL from a
// variant's is the per-site pattern invention extraction refuses. The bytes are already
// in the browser, so this costs no request against the origin: it cannot age the signed
// link, cannot trip the rate limiter the per-host probe cap exists to avoid, and is not
// the extra GET per candidate that ranking deliberately does not make.
//
// The read is SPAWNED rather than performed inline because the caller is chromedp's
// event goroutine. A CDP command issued from inside a listener waits for a reply that
// the same goroutine has to deliver, and that goroutine is blocked in the listener:
// the session deadlocks and the extraction produces nothing at all. The spawned read
// is bound to the session context, so tearing the session down ends it.
//
// Failure means unknown and nothing else. getResponseBody has no body to give for a
// redirect, for a request that never completed, or for one Chrome has already evicted,
// and a wording or availability change in the protocol may cost a log line its
// sharpness. It may never cost a candidate its place (see media.LadderUnknown).
func (c *collector) askForLadder(reqID network.RequestID) {
	if !c.claimBodyRead(reqID) {
		return
	}
	go func() {
		body, err := c.responseBody(reqID)
		if err != nil {
			slog.DebugContext(c.ctx, "response body unavailable, renditions unknown", "request", reqID, "error", err)
			return
		}
		c.noteLadder(reqID, ladderOf(string(body)))
	}()
}

// claimBodyRead decides whether one finished request is worth a body read and claims
// it. Every condition keeps a cost down rather than expressing policy: the request has
// to belong to a captured candidate (the page's own scripts and images are nothing to
// this), the answer has to be still missing, and the candidate has to be a playlist,
// because getResponseBody returns the WHOLE body and asking it for an mp4 candidate
// means pulling the film through the debugging protocol.
func (c *collector) claimBodyRead(reqID network.RequestID) bool {
	if reqID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, done := c.bodyRead[reqID]; done {
		return false
	}
	i := slices.IndexFunc(c.candidates, func(cand candidate) bool { return cand.reqID == reqID })
	if i < 0 || c.candidates[i].ladder != media.LadderUnknown || !c.candidates[i].isPlaylist() {
		return false
	}
	c.bodyRead[reqID] = struct{}{}
	return true
}

// responseBody asks the browser for bytes it already holds. The executor comes from the
// session context because that is where chromedp keeps the connected target; a context
// carrying none is a session that never finished starting, which is unknown like any
// other unavailable body rather than a panic inside a listener's goroutine.
func (c *collector) responseBody(reqID network.RequestID) ([]byte, error) {
	cdpCtx := chromedp.FromContext(c.ctx)
	if cdpCtx == nil || cdpCtx.Target == nil {
		return nil, fmt.Errorf("no CDP target on the session context")
	}
	return network.GetResponseBody(reqID).Do(cdp.WithExecutor(c.ctx, cdpCtx.Target))
}

// noteLadder records what one document said. It is keyed on the request ID because
// that is all a finished load carries: the event has no URL on it.
func (c *collector) noteLadder(reqID network.RequestID, ladder media.Ladder) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.IndexFunc(c.candidates, func(cand candidate) bool { return cand.reqID == reqID })
	if i < 0 {
		return
	}
	c.candidates[i].ladder = ladder
	slog.InfoContext(c.ctx, "read captured playlist", "url", c.candidates[i].rawURL, "renditions", ladder)
}

// Wait blocks until streams are found or the grace period expires.
// If streams are already captured, it waits collectionWindow for more, then returns.
// If no streams are found, it waits up to graceAfterActions before giving up.
func (c *collector) Wait(ctx context.Context, graceAfterActions, collectionWindow time.Duration) ([]capturedStream, error) {
	collectMore := func() []capturedStream {
		// Once a master playlist is in hand there's nothing better to wait for
		// (it already enumerates every variant), so return immediately instead of
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
		// Page-set headers only. The browser-added Referer/Origin/Cookie that
		// hotlinked CDNs check arrive separately, in the ExtraInfo event below;
		// both merge by request ID.
		c.mergeHeaders(e.RequestID, toHTTPHeader(e.Request.Headers))
		c.addByPattern(e.Request.URL, e.RequestID)

	case *network.EventRequestWillBeSentExtraInfo:
		// The real on-the-wire headers (Referer, Origin, Cookie, sec-ch-*).
		c.mergeHeaders(e.RequestID, toHTTPHeader(e.Headers))

	case *network.EventResponseReceived:
		c.addByMIME(e.Response.URL, e.RequestID, e.Response.MimeType)

	case *network.EventLoadingFinished:
		// This event, and not responseReceived, is when a body read is worth
		// attempting. responseReceived fires with the headers, while the document is
		// still arriving, and getResponseBody answers "No data found for resource with
		// given identifier" for a request that has not finished. After the load the body
		// lives on borrowed time: a navigation, an iframe teardown or memory pressure
		// evicts it, and an m3u8 fetched by a player inside an iframe is exactly the
		// shape that gets torn down. Loading-finished is the one moment both are false.
		c.askForLadder(e.RequestID)

	case *runtime.EventConsoleAPICalled:
		for _, arg := range e.Args {
			val := strings.Trim(string(arg.Value), `"`)
			for _, m := range hlsURLPattern.FindAllString(val, -1) {
				c.addByPattern(m, "")
			}
		}
	}
}

// mergeHeaders folds outgoing headers into the set recorded for a request ID.
// requestWillBeSent and ExtraInfo each call it once, in either order; non-empty
// values win so neither clobbers the other's contribution.
func (c *collector) mergeHeaders(id network.RequestID, headers http.Header) {
	if id == "" || len(headers) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	existing := c.requestHeaders[id]
	if existing == nil {
		existing = make(http.Header, len(headers))
		c.requestHeaders[id] = existing
	}
	for k, vs := range headers {
		if len(vs) > 0 && vs[0] != "" {
			existing[k] = vs
		}
	}
}

// toHTTPHeader converts CDP headers into an http.Header (canonical keys),
// skipping HTTP/2 pseudo-headers (":authority", ":method", …) that an outgoing
// request can't carry. Nil when nothing usable remains.
func toHTTPHeader(h network.Headers) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		if s, ok := v.(string); ok && !strings.HasPrefix(k, ":") {
			out.Set(k, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// masterScore is the rankURL bonus for a path containing "master". It is a hint about
// URL QUALITY and nothing more: a publisher naming a path "master" usually means it,
// which is worth an ordering nudge among documents nothing else separates. It is not
// the definition of a master, because a path is not a document: the definition is the
// #EXT-X-STREAM-INF tag inside the body (see ladderOf), and a confirmed ladder is
// compared ahead of any of these bonuses (see Entries).
const masterScore = 100

// rankURL assigns a score to a captured URL, higher being preferred. Every signal here
// is a guess read off a path, so it only ever breaks ties between documents whose own
// grammar castor could not read.
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
