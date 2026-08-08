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

	// Navigate with a timeout, but don't use a child context: canceling a
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
		// A navigation error with something already captured is survivable: the page
		// requested a stream before it finished loading, and a slow tracker or an
		// aborted subresource is not a reason to throw that away. This asks whether
		// anything at all arrived, which is a different question from the action
		// pipeline's and the collection window's shared "is there anything better left
		// to wait for" (collector.hasMaster). Here the alternative is no session and no
		// candidates whatsoever, so any hit clears the bar.
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

// action is one best-effort nudge at the page: a name for the log and the work.
type action struct {
	name string
	do   func() error
}

// RunActions drives the page until it has been driven as far as it can usefully be
// driven, which is either a captured master playlist or the end of the action list.
func (s *session) RunActions(actionCfg ActionConfig) {
	snapshot(s.ctx, s.snapshotDir, "pipeline_start")

	actions := []action{
		{"click", func() error { return click(s.ctx, s.centerX, s.centerY) }},
		{"navigate iframe", func() error {
			return navigateIframe(s.ctx, actionCfg.NavigateIframeTimeout, actionCfg.NavigateIframeMaxDepth)
		}},
		{"bypass turnstile", func() error {
			return bypassTurnstile(s.ctx, actionCfg.BypassTurnstileTimeout, actionCfg.TurnstileRetryTimeout)
		}},
		{"click", func() error { return click(s.ctx, s.centerX, s.centerY) }},
	}

	ran := runActions(s.ctx, s.collector, actions, func(i int) {
		snapshot(s.ctx, s.snapshotDir, fmt.Sprintf("step_%d", i))
	})

	// Exhausted or cut short is the first thing worth knowing when a diagnosis starts
	// from a capture that turned out to hold a single rendition: cut short says a master
	// was already in hand, exhausted says the page was driven all the way and never
	// offered one.
	slog.DebugContext(s.ctx, "action pipeline finished", "actions_run", ran, "actions", len(actions))
}

// runActions walks a bounded list of page actions in order, running each one whose
// turn comes and logging its failure at DEBUG rather than stopping: the page that
// needs a click is rarely the page that needs the iframe descent, so a step that
// finds nothing to do must not deny the next one its turn. observe is called after
// every action that ran, and the count of actions run is returned so the caller can
// tell an exhausted list from a list cut short.
//
// The only early exit is collector.hasMaster, the same test the collection window
// stops on, read here rather than written again. It is deliberately not "anything at
// all was captured", which is what this loop used to stop on, sixty lines away from
// the window's master test and mentioning neither it nor why they differed. A player
// that requested its 2160p media playlist during the opening click satisfied that
// weaker test, so the iframe descent and the turnstile bypass never ran and the page
// was never driven far enough to ask for the master. What reached the ranker was one
// rendition, 3840x1600 at 18505 kb/s, with no ladder behind it: when the link then
// delivered 7.2 Mbit/s (0.39x realtime) and stopped, there was nothing lighter to fall
// back to. Stopping only on a master keeps the instinct that motivated the early exit
// (these are short-lived signed links and a master is the best thing there is to hold,
// so stop once one is in hand) without stranding the whole extraction on a chunklist.
func runActions(ctx context.Context, c *collector, actions []action, observe func(i int)) int {
	for i, a := range actions {
		if c.hasMaster() {
			return i
		}
		if err := a.do(); err != nil {
			slog.DebugContext(ctx, a.name+" failed", "error", err)
		}
		observe(i)
	}
	return len(actions)
}

func (s *session) Close() {
	// Cancelling these only *signals* teardown; chromedp kills the Chrome
	// process and reaps it in a background goroutine (see ExecAllocator).
	s.cancel()
	s.allocCancel()

	// Block until that goroutine has actually reaped the process. Without this
	// wait, an abrupt exit (e.g. Ctrl-C mid-extraction, when main returns as
	// soon as the root context is cancelled) can outrun the async kill and
	// orphan the headless browser to launchd, where it keeps autoplaying the
	// stream's audio with no window. Waiting also lets chromedp delete the
	// temporary user-data-dir it created for the session.
	if c := chromedp.FromContext(s.ctx); c != nil && c.Allocator != nil {
		c.Allocator.Wait()
	}
}
