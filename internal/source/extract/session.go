// Castor is a proof of concept provided for lawful, personal, and educational
// use. This file is part of its stream-extraction pipeline and is intended only
// for accessing content you are authorized to view. Do not use it to infringe
// copyright or to circumvent access controls. The author does not endorse or
// condone piracy. See the "Purpose and disclaimer" section of the README.

package extract

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// session owns the chromedp lifecycle for a single proxy attempt.
type session struct {
	ctx         context.Context
	cancel      context.CancelFunc
	allocCancel context.CancelFunc
	collector   *collector
	centerX     float64
	centerY     float64
	snapshotDir string
}

// newSession creates a browser session: allocator, stealth injection,
// navigation, and event listeners. It returns a ready-to-use Session.
func newSession(ctx context.Context, e *Extractor, targetURL string) (*session, error) {
	profile := NewProfile()

	opts := allocatorOpts(e.browser, profile)

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)

	taskCtx, taskCancel := chromedp.NewContext(allocCtx)

	collector := newCollector(taskCtx, e.patterns, e.capture.MaxCandidates)

	chromedp.ListenTarget(taskCtx, collector.Listen)

	// Navigate with a timeout, but don't use a child context — canceling a
	// child of the chromedp task context breaks the target in chromedp v0.14.
	navDone := make(chan error, 1)
	go func() {
		navDone <- chromedp.Run(taskCtx,
			runtime.Enable(),
			network.Enable(),
			browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorDeny),
			injectStealth(profile),
			injectCDPStealth(profile),
			chromedp.Navigate(targetURL),
		)
	}()

	var err error
	select {
	case err = <-navDone:
		// Navigation completed (success or error).
	case <-time.After(e.browser.Timeout):
		err = fmt.Errorf("navigation timed out after %s", e.browser.Timeout)
	}

	if err != nil {
		// If navigation failed but we already captured URLs, keep going.
		if !collector.HasHits() {
			taskCancel()
			allocCancel()
			return nil, err
		}
	}

	snapDir := filepath.Join(".debug", sanitize(targetURL))
	snapshot(taskCtx, snapDir, "after_nav")

	return &session{
		ctx:         taskCtx,
		cancel:      taskCancel,
		allocCancel: allocCancel,
		collector:   collector,
		centerX:     profile.CenterX,
		centerY:     profile.CenterY,
		snapshotDir: snapDir,
	}, nil
}

// RunActions executes the action pipeline, skipping remaining steps once URLs
// are captured. Each step is best-effort: failures are logged at DEBUG and
// the next step still runs.
func (s *session) RunActions(actionCfg ActionConfig) {
	snapshot(s.ctx, s.snapshotDir, "pipeline_start")

	steps := []struct {
		name string
		do   func() error
	}{
		{"click", func() error { return click(s.ctx, s.centerX, s.centerY) }},
		{"navigate iframe", func() error {
			return navigateIframe(s.ctx, actionCfg.NavigateIframeTimeout, actionCfg.NavigateIframeMaxDepth)
		}},
		{"bypass turnstile", func() error {
			return bypassTurnstile(s.ctx, actionCfg.BypassTurnstileTimeout, actionCfg.TurnstileRetryTimeout)
		}},
		{"click", func() error { return click(s.ctx, s.centerX, s.centerY) }},
	}

	for i, step := range steps {
		if s.collector.HasHits() {
			return
		}
		if err := step.do(); err != nil {
			slog.DebugContext(s.ctx, step.name+" failed", "error", err)
		}
		snapshot(s.ctx, s.snapshotDir, fmt.Sprintf("step_%d", i))
	}
}

func (s *session) Close() {
	// Cancelling these only *signals* teardown; chromedp kills the Chrome
	// process and reaps it in a background goroutine (see ExecAllocator).
	s.cancel()
	s.allocCancel()

	// Block until that goroutine has actually reaped the process. Without this
	// wait, an abrupt exit — e.g. Ctrl-C mid-extraction, when main returns as
	// soon as the root context is cancelled — can outrun the async kill and
	// orphan the headless browser to launchd, where it keeps autoplaying the
	// stream's audio with no window. Waiting also lets chromedp delete the
	// temporary user-data-dir it created for the session.
	if c := chromedp.FromContext(s.ctx); c != nil && c.Allocator != nil {
		c.Allocator.Wait()
	}
}
