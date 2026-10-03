package judge

import (
	"net/url"
	"slices"
	"time"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/receiver"
)

// Evidence is everything one cast left behind.
type Evidence struct {
	Origin   *origin.Origin
	Endpoint receiver.Endpoint
	Viewer   receiver.Viewer
	Received receiver.Received
	// Handed is whether castor handed the receiver anything at all.
	Handed bool
	// CastErr is how castor's process exited, and Exited when.
	CastErr error
	Exited  time.Time
	// Killed is a castor that never ended on its own, stopped by the suite's deadline.
	Killed bool
	// Ceiling is the tallest picture castor was allowed to deliver.
	Ceiling int
}

// ReceiverFetchedOrigin reports whether the receiver reached the origin itself rather than through castor.
func (e Evidence) ReceiverFetchedOrigin() bool {
	return slices.ContainsFunc(e.Origin.Requests(), func(r origin.Request) bool { return r.UserAgent == receiver.UserAgent })
}

// HandedOrigin reports whether the receiver was handed a URL on the origin rather than castor's relay.
func (e Evidence) HandedOrigin() bool {
	handed, err := url.Parse(e.Received.URL)
	source, _ := url.Parse(e.Origin.URL)
	return err == nil && handed.Host == source.Host
}
