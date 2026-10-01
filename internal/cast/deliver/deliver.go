// Package deliver is how a cast reaches its renderer: a progressive stream or a live HLS directory, the spool its bytes pass through, and the terms both open on.
package deliver

import (
	"io"
	"time"

	"github.com/stupside/castor/internal/cast/container"
)

// SettleInterval is how often a delivery's Wait re-reads whether it still has anything to do.
const SettleInterval = 500 * time.Millisecond

type Artifact struct {
	// Subject names it in a log line and in a fault ("the stream output", "the HLS playlist").
	Subject string

	Landed func() int64

	Grace time.Duration
}

// Opening is everything a delivery mechanism is opened with.
type Opening struct {
	Format    container.FormatInfo
	Listeners Listeners
	Dir       string
	Out       io.Reader
	Headers   map[string]string

	// IdleGrace is how long an idle renderer is waited for before the delivery counts as done.
	IdleGrace time.Duration

	// WriteDeadline bounds one socket write, so a hung client cannot hold its goroutine.
	WriteDeadline time.Duration
}
