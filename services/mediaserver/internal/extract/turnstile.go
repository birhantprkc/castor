package extract

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chromedp/chromedp"
)

// turnstileSelector names the challenge widget for every script that looks for it.
const turnstileSelector = ".cf-turnstile"

//go:embed js/turnstile_iframe_pos.js
var turnstileIframePosTemplate string

//go:embed js/turnstile_gone.js
var turnstileGoneTemplate string

var (
	turnstileIframePosJS = strings.ReplaceAll(turnstileIframePosTemplate, "__TURNSTILE__", turnstileSelector)
	turnstileGoneJS      = strings.ReplaceAll(turnstileGoneTemplate, "__TURNSTILE__", turnstileSelector)
)

func detectTurnstile(ctx context.Context) bool {
	var present bool
	if err := chromedp.Run(ctx, chromedp.Evaluate("!("+turnstileGoneJS+")", &present)); err != nil {
		return false
	}
	return present
}

func solveTurnstile(ctx context.Context) bool {
	tCtx, cancel := context.WithTimeout(ctx, bypassTurnstileTimeout)
	defer cancel()

	ch := make(chan struct{}, 2)

	go func() {
		var pos map[string]any
		if err := chromedp.Run(tCtx,
			chromedp.Poll(turnstileIframePosJS, &pos, chromedp.WithPollingTimeout(0)),
		); err != nil {
			slog.DebugContext(ctx, "error polling for turnstile iframe", "error", err)
			return
		}

		x, _ := pos["x"].(float64)
		y, _ := pos["y"].(float64)

		var gone bool
		if err := chromedp.Run(tCtx,
			chromedp.MouseClickXY(x, y, chromedp.ButtonLeft),
			chromedp.Poll(turnstileGoneJS, &gone, chromedp.WithPollingTimeout(0)),
			chromedp.WaitReady("body"),
		); err != nil {
			slog.DebugContext(ctx, "error solving turnstile", "error", err)
			return
		}
		ch <- struct{}{}
	}()

	// Passive: handles auto-solve token fill and the cftCallback page reload.
	go func() {
		var gone bool
		if err := chromedp.Run(tCtx,
			chromedp.Poll(turnstileGoneJS, &gone, chromedp.WithPollingTimeout(0)),
			chromedp.WaitReady("body"),
		); err != nil {
			slog.DebugContext(ctx, "error polling for turnstile gone", "error", err)
			return
		}
		ch <- struct{}{}
	}()

	select {
	case <-ch:
		cancel()
		return true
	case <-tCtx.Done():
		return false
	}
}

func bypassTurnstile(ctx context.Context) error {
	if !detectTurnstile(ctx) {
		return nil
	}

	if solveTurnstile(ctx) {
		return nil
	}
	slog.DebugContext(ctx, "initial turnstile solve attempt failed, retrying after reload")

	retryCtx, retryCancel := context.WithTimeout(ctx, turnstileRetryTimeout)
	defer retryCancel()
	if err := chromedp.Run(retryCtx, chromedp.Reload(), chromedp.WaitReady("body")); err != nil {
		return fmt.Errorf("turnstile reload failed: %w", err)
	}

	if detectTurnstile(ctx) {
		if !solveTurnstile(ctx) {
			return fmt.Errorf("turnstile solve failed after retry")
		}
		slog.DebugContext(ctx, "turnstile solved after retry")
	}

	return nil
}
