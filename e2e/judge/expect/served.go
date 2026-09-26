// Package expect holds the checks a case names for what its cast must do.
package expect

import (
	"fmt"
	"time"

	"github.com/stupside/castor/e2e/judge"
)

// Served holds that castor relayed the stream, the receiver never reached the origin, and castor left once playback ended.
type Served struct{}

func (Served) Name() string { return "served" }

func (Served) Judge(e judge.Evidence) []string {
	lag := e.Exited.Sub(e.Received.Ended)
	return append(
		judge.FailIf(e.HandedOrigin() || e.ReceiverFetchedOrigin(),
			fmt.Sprintf("handed %s, want castor to relay and the receiver never to reach the origin", e.Received.URL)),
		judge.FailIf(lag > e.Endpoint.EndsWithin, fmt.Sprintf("castor kept running %v after the receiver's playback ended", lag.Round(time.Second)))...)
}
