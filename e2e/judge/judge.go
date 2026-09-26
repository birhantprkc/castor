// Package judge holds a cast to its case: an outcome, invariants every played cast answers to, and expectations a case names.
package judge

import (
	"errors"
	"net/url"
	"time"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/receiver"
	"github.com/stupside/castor/e2e/strategy"
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

// Check is one property of a cast; it returns every way the evidence breaks it.
type Check interface {
	strategy.Named
	Judge(e Evidence) []string
}

// Outcome is how a cast ends; it decides which of a case's checks its evidence can answer.
type Outcome interface {
	Check
	Scope(invariants, expectations []Check) ([]Check, error)
}

// ReceiverFetchedOrigin reports whether the receiver reached the origin itself rather than through castor.
func (e Evidence) ReceiverFetchedOrigin() bool {
	for _, r := range e.Origin.Requests() {
		if r.UserAgent == receiver.UserAgent {
			return true
		}
	}
	return false
}

// HandedOrigin reports whether the receiver was handed a URL on the origin rather than castor's relay.
func (e Evidence) HandedOrigin() bool {
	handed, err := url.Parse(e.Received.URL)
	source, _ := url.Parse(e.Origin.URL)
	return err == nil && handed.Host == source.Host
}

// FailIf is the failure message when broken holds, and nothing when it does not.
func FailIf(broken bool, message string) []string {
	if broken {
		return []string{message}
	}
	return nil
}

// OnlyWhenPlayed refuses expectations on an outcome whose evidence has no played stream to judge them on.
func OnlyWhenPlayed(expectations []Check) error {
	if len(expectations) > 0 {
		return errors.New("expect: a cast that does not play has nothing to hold expectations to; drop them or expect outcome plays")
	}
	return nil
}

// DurationSlack is one segment either side: a cut lands on a keyframe, not on the second.
const DurationSlack = 1500 * time.Millisecond
