// Castor is a proof of concept provided for lawful, personal, and educational
// use. This file is part of its stream-extraction pipeline and is intended only
// for accessing content you are authorized to view. Do not use it to infringe
// copyright or to circumvent access controls. The author does not endorse or
// condone piracy. See the "Purpose and disclaimer" section of the README.

package extract

import "time"

// Config is everything an Extractor needs. It is defined here — not in an
// application-level package — so the dependency arrow points the right way:
// the app config composes this type, never the reverse.
type Config struct {
	Browser BrowserConfig
	Capture CaptureConfig
	Actions ActionConfig
}

type BrowserConfig struct {
	Timeout    time.Duration `yaml:"timeout" validate:"required"`
	Headless   bool          `yaml:"headless"`
	NoSandbox  bool          `yaml:"no_sandbox"`
	ChromePath string        `yaml:"chrome_path"`
}

type CaptureConfig struct {
	Patterns          []string      `yaml:"patterns" validate:"required,min=1"`
	MaxCandidates     int           `yaml:"max_candidates" validate:"required,min=1"`
	MaxConcurrency    int           `yaml:"max_concurrency" validate:"required,min=1"`
	CollectionWindow  time.Duration `yaml:"collection_window" validate:"required"`
	GraceAfterActions time.Duration `yaml:"grace_after_actions" validate:"required"`
}

type ActionConfig struct {
	TurnstileRetryTimeout  time.Duration `yaml:"turnstile_retry_timeout" validate:"required"`
	NavigateIframeTimeout  time.Duration `yaml:"navigate_iframe_timeout" validate:"required"`
	NavigateIframeMaxDepth int           `yaml:"navigate_iframe_max_depth" validate:"required,min=1"`
	BypassTurnstileTimeout time.Duration `yaml:"bypass_turnstile_timeout" validate:"required"`
}
