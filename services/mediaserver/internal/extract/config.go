package extract

import (
	"net/url"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/source"
)

// Config is everything an Extractor needs.
type Config struct {
	Browser BrowserConfig
	Capture CaptureConfig

	// Documents reads every grammar a captured body may be written in.
	Documents Documents
}

// Documents names what a captured link carries and reads the documents it fetched.
type Documents interface {
	ContentTypeOf(u *url.URL, mime string) string
	Parse(body string, base *url.URL) source.Document
}

type BrowserConfig struct {
	Timeout    time.Duration `yaml:"timeout" validate:"required"`
	Headless   bool          `yaml:"headless"`
	NoSandbox  bool          `yaml:"no_sandbox"`
	ChromePath string        `yaml:"chrome_path"`
}

type CaptureConfig struct {
	// MaxConcurrency bounds how many page URLs are extracted at once, one browser each.
	MaxConcurrency int `yaml:"max_concurrency" validate:"required,min=1"`
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
	snapshotTimeout        = 5 * time.Second
	pageBudget             = 2 * time.Minute
)
