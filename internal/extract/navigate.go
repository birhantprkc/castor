package extract

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// iframeSrcJS finds largest visible iframe (100x100+) and returns src.
//
//go:embed js/iframe_src.js
var iframeSrcJS string

// navigateIframe polls for largest iframe and navigates; repeat until leaf.
func navigateIframe(ctx context.Context) error {
	iframeCtx, cancel := context.WithTimeout(ctx, navigateIframeTimeout)
	defer cancel()

	for depth := range navigateIframeMaxDepth {
		var iframeSrc, parent string

		err := chromedp.Run(iframeCtx,
			chromedp.Poll(iframeSrcJS, &iframeSrc, chromedp.WithPollingTimeout(0)),
			chromedp.Location(&parent),
			chromedp.ActionFunc(func(ctx context.Context) error {
				slog.DebugContext(ctx, "navigating to iframe", "src", iframeSrc, "depth", depth+1)
				return openFrame(ctx, iframeSrc, parent)
			}),
			chromedp.WaitReady("body"),
		)

		if err != nil {
			if depth == 0 {
				return err // No iframe found at all, a real error
			}
			return nil // Reached leaf: no more iframes, that's fine
		}
	}

	return nil
}

// openFrame loads a frame's document as its parent would, since embed hosts refuse a request no site framed.
func openFrame(ctx context.Context, src, parent string) error {
	_, _, errorText, _, err := page.Navigate(src).
		WithReferrer(parent).
		WithReferrerPolicy(page.ReferrerPolicyStrictOriginWhenCrossOrigin).
		Do(ctx)
	if err != nil {
		return err
	}
	if errorText != "" {
		return fmt.Errorf("page load error %s", errorText)
	}
	return nil
}
