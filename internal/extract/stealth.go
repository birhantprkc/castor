package extract

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

//go:embed js/stealth_tostring.js
var stealthToStringJS string

//go:embed js/stealth_plugins.js
var stealthPluginsJS string

//go:embed js/stealth_chrome.js
var stealthChromeJS string

//go:embed js/stealth_permissions.js
var stealthPermissionsJS string

//go:embed js/stealth_webgl.js
var stealthWebGLJS string

//go:embed js/stealth_device_memory.js
var stealthDeviceMemoryJS string

//go:embed js/stealth_notification.js
var stealthNotificationJS string

//go:embed js/stealth_screen.js
var stealthScreenJS string

//go:embed js/stealth_webrtc.js
var stealthWebRTCJS string

//go:embed js/stealth_canvas.js
var stealthCanvasJS string

//go:embed js/stealth_audio.js
var stealthAudioJS string

//go:embed js/stealth_client_rects.js
var stealthClientRectsJS string

//go:embed js/stealth_font_metric.js
var stealthFontMetricJS string

//go:embed js/stealth_stack_trace.js
var stealthStackTraceJS string

func buildStealthJS(profile *profile) string {
	snippets := []string{
		stealthToStringJS,
		stealthPluginsJS,
		stealthChromeJS,
		stealthPermissionsJS,
		stealthWebGLJS,
		stealthDeviceMemoryJS,
		stealthNotificationJS,
		stealthScreenJS,
		stealthWebRTCJS,
		stealthCanvasJS,
		stealthAudioJS,
		stealthClientRectsJS,
		stealthFontMetricJS,
		stealthStackTraceJS,
	}
	// One closure, so the helpers the snippets share never reach the page's globals.
	joined := "(() => {\n" + strings.Join(snippets, "\n") + "\n})();"

	r := strings.NewReplacer(
		"__DEVICE_MEMORY__", fmt.Sprintf("%d", profile.deviceMemory),
		"__COLOR_DEPTH__", fmt.Sprintf("%d", profile.colorDepth),
		"__WEBGL_VENDOR__", profile.webGLVendor,
		"__WEBGL_RENDERER__", profile.webGLRenderer,
		"__NOISE_SEED__", fmt.Sprintf("%d", profile.noiseSeed),
		"__FONT_NOISE_PX__", fmt.Sprintf("%.6f", profile.fontNoisePx),
		"__RECT_NOISE_PX__", fmt.Sprintf("%.6f", profile.rectNoisePx),
		"__AUDIO_NOISE_MAG__", fmt.Sprintf("%.10f", profile.audioNoiseMag),
	)
	return r.Replace(joined)
}

// Returns exec-allocator options avoiding headless-detection flags.
func allocatorOpts(cfg BrowserConfig, profile *profile) []chromedp.ExecAllocatorOption {
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(cfg.ChromePath),

		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,

		chromedp.Flag("no-sandbox", cfg.NoSandbox),
		chromedp.Flag("disable-dev-shm-usage", true),

		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-infobars", true),
		chromedp.Flag("enable-features", "NetworkService,NetworkServiceInProcess"),
		chromedp.Flag("disable-features", "IsolateOrigins,site-per-process"),
		chromedp.Flag("disable-site-isolation-trials", true),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("webrtc-ip-handling-policy", "disable_non_proxied_udp"),

		chromedp.Flag("autoplay-policy", "no-user-gesture-required"),
		chromedp.Flag("incognito", true),

		chromedp.WindowSize(profile.screenWidth, profile.screenHeight),
	}

	// Only append when wanted; presence matters, not value.
	if cfg.Headless {
		opts = append(opts, chromedp.Flag("headless", "new"))
	}

	return opts
}

// identifyBrowser pins the profile's user agent to the version of the browser actually running.
func identifyBrowser(profile *profile) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		_, product, _, _, _, err := browser.GetVersion().Do(ctx)
		if err != nil {
			return fmt.Errorf("asking the browser its version: %w", err)
		}
		return profile.identify(product)
	}
}

// injectStealth injects the stealth script before any page JS runs.
func injectStealth(profile *profile) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		js := buildStealthJS(profile)
		_, err := page.AddScriptToEvaluateOnNewDocument(js).Do(ctx)
		return err
	}
}

// CDP-level overrides for automation signals JS can't mask.
func injectCDPStealth(profile *profile) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		if err := emulation.SetAutomationOverride(false).Do(ctx); err != nil {
			return err
		}

		if err := emulation.SetFocusEmulationEnabled(true).Do(ctx); err != nil {
			return err
		}

		if err := emulation.SetHardwareConcurrencyOverride(profile.hardwareConcurrency).Do(ctx); err != nil {
			return err
		}

		if err := emulation.SetTimezoneOverride(profile.timezoneID).Do(ctx); err != nil {
			return err
		}

		locale := profile.languages[0]
		if err := emulation.SetLocaleOverride().WithLocale(locale).Do(ctx); err != nil {
			return err
		}

		ua := emulation.SetUserAgentOverride(profile.userAgent)
		ua.AcceptLanguage = profile.acceptLanguage
		ua.Platform = profile.navigatorPlatform

		brands := make([]*emulation.UserAgentBrandVersion, len(profile.brands))
		for i, b := range profile.brands {
			brands[i] = &emulation.UserAgentBrandVersion{Brand: b[0], Version: b[1]}
		}
		fullVersionList := make([]*emulation.UserAgentBrandVersion, len(profile.fullVersionList))
		for i, b := range profile.fullVersionList {
			fullVersionList[i] = &emulation.UserAgentBrandVersion{Brand: b[0], Version: b[1]}
		}

		ua.UserAgentMetadata = &emulation.UserAgentMetadata{
			Brands:          brands,
			FullVersionList: fullVersionList,
			Platform:        profile.platform,
			PlatformVersion: profile.platformVersion,
			Architecture:    profile.architecture,
			Model:           "",
			Mobile:          false,
			Bitness:         profile.bitness,
		}
		return ua.Do(ctx)
	}
}
