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

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// urlInText finds the absolute links a page player prints to its console.
var urlInText = regexp.MustCompile(`https?://[^\s"'<>]+`)

// bodyReader asks the browser for the bytes of a response it already holds.
type bodyReader func(network.RequestID) ([]byte, error)

type capture struct {
	raw         string
	url         *url.URL
	reqID       network.RequestID
	contentType string
	ladder      source.Ladder
}

// collector is what one page fetched that may be a stream, and what its documents said.
type collector struct {
	documents source.Formats
	readBody  bodyReader
	grace     time.Duration
	window    time.Duration

	mu             sync.Mutex
	captures       []capture
	requestHeaders map[network.RequestID]http.Header
	bodyRead       map[network.RequestID]struct{}
	responseURL    map[network.RequestID]string
	// named maps a resource to the capture whose document names it.
	named  map[string]string
	closed bool
	reads  sync.WaitGroup

	captured chan struct{}
	mastered chan struct{}
}

func newCollector(documents source.Formats, readBody bodyReader, grace, window time.Duration) *collector {
	return &collector{
		documents:      documents,
		readBody:       readBody,
		grace:          grace,
		window:         window,
		requestHeaders: make(map[network.RequestID]http.Header),
		bodyRead:       make(map[network.RequestID]struct{}),
		responseURL:    make(map[network.RequestID]string),
		named:          make(map[string]string),
		captured:       make(chan struct{}),
		mastered:       make(chan struct{}),
	}
}

// addByURL records a link whose name says it is a segmented manifest.
func (c *collector) addByURL(raw string, reqID network.RequestID) {
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	if ct := c.documents.ContentTypeOf(u, ""); media.IsSegmented(ct) {
		c.add(raw, u, reqID, ct)
	}
}

// addByMIME records a response whose server-confirmed MIME type is a stream type.
func (c *collector) addByMIME(raw string, reqID network.RequestID, mime string) {
	if c.documents.ContentTypeOf(nil, mime) == "" {
		return
	}
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	c.add(raw, u, reqID, c.documents.ContentTypeOf(u, mime))
}

func (c *collector) add(raw string, u *url.URL, reqID network.RequestID, contentType string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if i := c.index(raw); i >= 0 {
		if c.captures[i].reqID == "" && reqID != "" {
			c.captures[i].reqID = reqID
			slog.Debug("attached request headers to captured URL", "url", raw)
		}
		return
	}
	if len(c.captures) >= maxCaptures {
		slog.Debug("capture limit reached, skipping URL", "url", raw)
		return
	}

	slog.Info("captured stream", "url", raw, "content_type", contentType)
	c.captures = append(c.captures, capture{raw: raw, url: u, reqID: reqID, contentType: contentType})
	closeOnce(c.captured)
}

func (c *collector) index(raw string) int {
	return slices.IndexFunc(c.captures, func(cp capture) bool { return cp.raw == raw })
}

// Entries is every capture in the order it was made, less those another capture's document names.
func (c *collector) Entries() []*source.Candidate {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []*source.Candidate
	for _, cp := range c.captures {
		if _, part := c.named[cp.raw]; part {
			continue
		}
		out = append(out, &source.Candidate{
			URL: cp.url,
			// Normalized under the lock: the listener keeps merging into this map while the caller reads it.
			Headers:     source.NormalizeStreamHeaders(c.requestHeaders[cp.reqID]),
			ContentType: cp.contentType,
			Ladder:      cp.ladder,
		})
	}
	return out
}

// HasHits reports whether anything at all was captured.
func (c *collector) HasHits() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.captures) > 0
}

// hasMaster reports whether a document advertising renditions has been captured.
func (c *collector) hasMaster() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(c.captures, func(cp capture) bool { return cp.ladder == source.LadderMultivariant })
}

// askForDocument reads a finished response as a document, once, on a read the collector joins on close.
func (c *collector) askForDocument(reqID network.RequestID, size float64) {
	if !c.claimBodyRead(reqID, size) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.reads.Go(func() {
		body, err := c.readBody(reqID)
		if err != nil {
			slog.Debug("response body unavailable, renditions unknown", "request", reqID, "error", err)
			return
		}
		c.noteDocument(reqID, string(body))
	})
}

// close waits out every body read in flight; none starts after it.
func (c *collector) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.reads.Wait()
}

// documentSizeLimit is how large a response may be and still be worth reading as a document.
const documentSizeLimit = 2 << 20

// claimBodyRead decides whether one finished request is worth reading as a document and claims it.
func (c *collector) claimBodyRead(reqID network.RequestID, size float64) bool {
	if reqID == "" || size > documentSizeLimit {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, done := c.bodyRead[reqID]; done {
		return false
	}
	i := c.captureFor(reqID)
	if i < 0 || c.captures[i].ladder != source.LadderUnknown {
		return false
	}
	c.bodyRead[reqID] = struct{}{}
	return true
}

// noteResponseURL records where one request ended.
func (c *collector) noteResponseURL(id network.RequestID, u string) {
	if id == "" || u == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.responseURL[id] = u
}

// captureFor resolves the capture a finished request's body belongs to. Callers hold the lock.
func (c *collector) captureFor(reqID network.RequestID) int {
	if u := c.responseURL[reqID]; u != "" {
		if i := c.index(u); i >= 0 {
			return i
		}
	}
	return slices.IndexFunc(c.captures, func(cp capture) bool { return cp.reqID == reqID })
}

func (c *collector) noteDocument(reqID network.RequestID, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.captureFor(reqID)
	if i < 0 {
		return
	}
	// The URL the body was fetched from, which is what its references resolve against.
	base, err := url.Parse(cmp.Or(c.responseURL[reqID], c.captures[i].raw))
	if err != nil {
		return
	}
	ladder, names := c.documents.Parse(body, base)
	c.captures[i].ladder = ladder
	for _, u := range names {
		if name := u.String(); name != c.captures[i].raw {
			c.named[name] = c.captures[i].raw
		}
	}
	if ladder == source.LadderMultivariant {
		closeOnce(c.mastered)
	}
	slog.Info("read captured document", "url", c.captures[i].raw, "renditions", ladder, "names", len(names))
}

// Wait gives the page its grace to fetch a stream, then its window to fetch a master, and returns what it fetched.
func (c *collector) Wait(ctx context.Context) ([]*source.Candidate, error) {
	grace := time.NewTimer(c.grace)
	defer grace.Stop()
	select {
	case <-c.captured:
	case <-grace.C:
	case <-ctx.Done():
	}

	if c.HasHits() {
		window := time.NewTimer(c.window)
		defer window.Stop()
		select {
		case <-c.mastered:
		case <-window.C:
		case <-ctx.Done():
		}
	}

	if entries := c.Entries(); len(entries) > 0 {
		return entries, nil
	}
	return nil, fmt.Errorf("no stream URL captured within grace period")
}

func (c *collector) Listen(ev any) {
	switch e := ev.(type) {
	case *network.EventRequestWillBeSent:
		// Page-set headers only.
		c.mergeHeaders(e.RequestID, toHTTPHeader(e.Request.Headers))
		c.addByURL(e.Request.URL, e.RequestID)

	case *network.EventRequestWillBeSentExtraInfo:
		// The real on-the-wire headers (Referer, Origin, Cookie, sec-ch-*).
		c.mergeHeaders(e.RequestID, toHTTPHeader(e.Headers))

	case *network.EventResponseReceived:
		c.noteResponseURL(e.RequestID, e.Response.URL)
		c.addByMIME(e.Response.URL, e.RequestID, e.Response.MimeType)

	case *network.EventLoadingFinished:
		// This event, and not responseReceived, is when a body read is worth attempting.
		c.askForDocument(e.RequestID, e.EncodedDataLength)

	case *runtime.EventConsoleAPICalled:
		for _, arg := range e.Args {
			for _, raw := range urlInText.FindAllString(strings.Trim(string(arg.Value), `"`), -1) {
				c.addByURL(raw, "")
			}
		}
	}
}

// mergeHeaders folds outgoing headers into the set recorded for a request ID.
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

func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}
