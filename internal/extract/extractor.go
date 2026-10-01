// Package extract captures video stream URLs from a page with headless Chrome.
package extract

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/stupside/castor/internal/source"
)

// Extractor opens pages in a browser and captures the streams they fetch.
type Extractor struct {
	browser   BrowserConfig
	parallel  int
	documents source.Formats
}

func New(cfg Config) *Extractor {
	return &Extractor{browser: cfg.Browser, parallel: cfg.Capture.ParallelURLs, documents: cfg.Documents}
}

func (e *Extractor) extract(ctx context.Context, targetURL string) ([]*source.Stream, error) {
	session, err := newSession(ctx, e.browser, e.documents, targetURL)
	if err != nil {
		return nil, fmt.Errorf("creating session for %s: %w", targetURL, err)
	}
	defer session.Close()

	session.runActions()

	streams, err := session.collector.Wait(ctx)
	if err != nil {
		return nil, fmt.Errorf("waiting for streams on %s: %w", targetURL, err)
	}
	return streams, nil
}

// ExtractAll extracts every url at once, within the parallelism the config allows.
func (e *Extractor) ExtractAll(ctx context.Context, urls []string) ([]*source.Stream, error) {
	slog.InfoContext(ctx, "extracting streams", "urls", len(urls))

	var wg sync.WaitGroup
	sem := make(chan struct{}, e.parallel)
	results := make([][]*source.Stream, len(urls))
	failures := make([]error, len(urls))

	for i, targetURL := range urls {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			slog.DebugContext(ctx, "extracting", "url", targetURL, "index", i+1, "total", len(urls))

			streams, err := e.extract(ctx, targetURL)
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

	deduped := deduplicateStreams(slices.Concat(results...))
	if len(deduped) == 0 {
		return nil, fmt.Errorf("no stream extracted from %d URL(s): %w", len(urls), errors.Join(failures...))
	}
	slog.InfoContext(ctx, "extraction complete", "urls", len(urls), "streams", len(deduped))
	return deduped, nil
}

func deduplicateStreams(streams []*source.Stream) []*source.Stream {
	seen := make(map[string]struct{}, len(streams))
	return slices.DeleteFunc(slices.Clone(streams), func(s *source.Stream) bool {
		key := s.URL.String()
		if _, ok := seen[key]; ok {
			return true
		}
		seen[key] = struct{}{}
		return false
	})
}
