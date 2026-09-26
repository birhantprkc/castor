package extract

import (
	"time"

	"github.com/stupside/castor/internal/source"
)

// Config is everything an Extractor needs (app config composes this, not reverse).
type Config struct {
	Browser BrowserConfig
	Capture CaptureConfig

	// Documents: every format whose grammar a captured body may be written in (composition bound).
	Documents source.Formats
}

type BrowserConfig struct {
	Timeout    time.Duration `yaml:"timeout" validate:"required"`
	Headless   bool          `yaml:"headless"`
	NoSandbox  bool          `yaml:"no_sandbox"`
	ChromePath string        `yaml:"chrome_path"`
}

type CaptureConfig struct {
	// ParallelURLs bounds how many page URLs are extracted at once, one browser each.
	ParallelURLs int `yaml:"max_concurrency" validate:"required,min=1"`
}

// Tuning castor ships: how long a page is given, and how far it is driven.
const (
	maxCaptures            = 100
	collectionWindow       = 10 * time.Second
	graceAfterActions      = 15 * time.Second
	preRollWindow          = 60 * time.Second
	navigateIframeTimeout  = 10 * time.Second
	navigateIframeMaxDepth = 5
	turnstileRetryTimeout  = 10 * time.Second
	bypassTurnstileTimeout = 20 * time.Second
)
