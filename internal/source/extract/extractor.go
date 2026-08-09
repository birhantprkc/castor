// Castor is a proof of concept provided for lawful, personal, and educational
// use. This file is part of its stream-extraction pipeline and is intended only
// for accessing content you are authorized to view. Do not use it to infringe
// copyright or to circumvent access controls. The author does not endorse or
// condone piracy. See the "Purpose and disclaimer" section of the README.

package extract

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"sync"

	"github.com/stupside/castor/internal/media"
)

// Extractor captures video stream URLs from a page using headless Chrome.
// It holds only capture and action config (patterns, timing), no proxies or templates.
type Extractor struct {
	browser  BrowserConfig
	capture  CaptureConfig
	actions  ActionConfig
	patterns []*regexp.Regexp
}

func New(cfg Config) (*Extractor, error) {
	e := &Extractor{
		browser: cfg.Browser,
		capture: cfg.Capture,
		actions: cfg.Actions,
	}

	for i, p := range cfg.Capture.Patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("pattern #%d: %w", i, err)
		}
		e.patterns = append(e.patterns, re)
	}

	return e, nil
}

// extract runs a single session+pipeline extraction attempt.
func (e *Extractor) extract(ctx context.Context, targetURL string) ([]*media.Stream, error) {
	session, err := newSession(ctx, e, targetURL)
	if err != nil {
		return nil, fmt.Errorf("creating session for %s: %w", targetURL, err)
	}
	defer session.Close()

	session.RunActions(e.actions)

	entries, err := session.collector.Wait(ctx, e.capture.GraceAfterActions, e.capture.CollectionWindow)
	if err != nil {
		return nil, fmt.Errorf("waiting for streams on %s: %w", targetURL, err)
	}

	streams := streamsFrom(ctx, entries)
	if len(streams) == 0 {
		return nil, fmt.Errorf("no usable streams found (%d entries captured, none with recognized content type)", len(entries))
	}

	return streams, nil
}

// streamsFrom turns what one session captured into the streams the ranker walks,
// dropping an entry whose container neither its extension nor its confirmed MIME could
// name, since nothing can be read from a source castor has no reader for.
//
// It stands apart from the session that produced them because the carrying is the part
// that has to be right: every fact here was established inside a browser that is about
// to be torn down, and the ladder in particular was read from a response body nothing
// downstream can ask for again.
func streamsFrom(ctx context.Context, entries []capturedStream) []*media.Stream {
	var streams []*media.Stream
	for _, entry := range entries {
		u, err := url.Parse(entry.RawURL)
		if err != nil {
			slog.DebugContext(ctx, "skipping entry, invalid URL", "raw_url", entry.RawURL, "error", err)
			continue
		}
		ct := contentTypeOf(u, entry.MimeType)
		if ct == "" {
			slog.DebugContext(ctx, "skipping entry, unknown content type", "url", u.String())
			continue
		}
		streams = append(streams, &media.Stream{
			URL:         u,
			Headers:     media.NormalizeStreamHeaders(entry.Headers),
			ContentType: ct,
			Ladder:      entry.Ladder,
		})
	}
	return streams
}

// ExtractAll runs one extraction per URL concurrently (bounded by the extractor's
// MaxConcurrency) and returns the deduplicated streams. The URLs are alternate embeds
// of the same title, so one of them succeeding is a success.
func (e *Extractor) ExtractAll(ctx context.Context, urls []string) ([]*media.Stream, error) {
	return e.extractAll(ctx, urls, e.extract)
}

// extractAll is ExtractAll over an injected per-URL extraction, which is what makes
// the all-failed reporting exercisable without launching a browser per URL.
func (e *Extractor) extractAll(ctx context.Context, urls []string, one func(context.Context, string) ([]*media.Stream, error)) ([]*media.Stream, error) {
	slog.InfoContext(ctx, "extracting streams", "urls", len(urls))

	var wg sync.WaitGroup
	sem := make(chan struct{}, e.capture.MaxConcurrency)
	results := make([][]*media.Stream, len(urls))
	failures := make([]error, len(urls))

	for i, targetURL := range urls {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			slog.DebugContext(ctx, "extracting", "url", targetURL, "index", i+1, "total", len(urls))

			streams, err := one(ctx, targetURL)
			if err != nil {
				slog.WarnContext(ctx, "extraction failed", "url", targetURL, "error", err)
				failures[i] = fmt.Errorf("%s: %w", targetURL, err)
				return
			}

			results[i] = streams
			slog.DebugContext(ctx, "extracted", "url", targetURL, "count", len(streams))
		})
	}
	wg.Wait()

	var allStreams []*media.Stream
	for _, ss := range results {
		allStreams = append(allStreams, ss...)
	}

	deduped := deduplicateStreams(allStreams)
	if len(deduped) == 0 {
		// Every embed failed, and the causes are the diagnosis: a navigation timeout, a
		// grace period that expired with nothing captured and a page whose only capture
		// had an unrecognised content type are three different things to do next. Returning
		// them joined is what stops that becoming the ranker's "no streams to rank" two
		// calls later, which reads as a bug in ranking and names nothing. Every empty
		// result has at least one cause recorded, because extract never answers (nil, nil):
		// a session that captured nothing usable returns why.
		return nil, fmt.Errorf("no stream extracted from %d URL(s): %w", len(urls), errors.Join(failures...))
	}
	slog.InfoContext(ctx, "extraction complete", "urls", len(urls), "streams", len(deduped))
	return deduped, nil
}

func deduplicateStreams(streams []*media.Stream) []*media.Stream {
	seen := make(map[string]struct{}, len(streams))
	return slices.DeleteFunc(slices.Clone(streams), func(s *media.Stream) bool {
		key := s.URL.String()
		if _, ok := seen[key]; ok {
			return true
		}
		seen[key] = struct{}{}
		return false
	})
}
