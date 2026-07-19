// Castor is a proof of concept provided for lawful, personal, and educational
// use. This file is part of its stream-extraction pipeline and is intended only
// for accessing content you are authorized to view. Do not use it to infringe
// copyright or to circumvent access controls. The author does not endorse or
// condone piracy. See the "Purpose and disclaimer" section of the README.

package extract

import (
	"context"
	_ "embed"
	"log/slog"
	"time"

	"github.com/chromedp/chromedp"
)

// iframeSrcJS finds the largest visible iframe (min 100x100) and returns its
// src URL. Returns null if no suitable iframe is found or src is empty/about:.
//
//go:embed js/iframe_src.js
var iframeSrcJS string

// navigateIframe polls for the largest iframe and navigates into it,
// repeating through nested iframes until no more are found.
func navigateIframe(ctx context.Context, timeout time.Duration, maxDepth int) error {
	iframeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for depth := range maxDepth {
		var iframeSrc string

		err := chromedp.Run(iframeCtx,
			chromedp.Poll(iframeSrcJS, &iframeSrc, chromedp.WithPollingTimeout(0)),
			chromedp.ActionFunc(func(ctx context.Context) error {
				slog.DebugContext(ctx, "navigating to iframe", "src", iframeSrc, "depth", depth+1)
				return chromedp.Navigate(iframeSrc).Do(ctx)
			}),
			chromedp.WaitReady("body"),
		)

		if err != nil {
			if depth == 0 {
				return err // No iframe found at all — real error
			}
			return nil // Reached leaf — no more iframes, that's fine
		}
	}

	return nil
}
