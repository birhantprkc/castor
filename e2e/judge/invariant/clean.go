// Package invariant holds the checks every played cast answers to.
package invariant

import "github.com/stupside/castor/e2e/judge"

// Clean holds the cast to the receiver's protocol: every violation the receiver saw is a failure.
type Clean struct{}

func (Clean) Name() string                    { return "protocol" }
func (Clean) Judge(e judge.Evidence) []string { return e.Received.Problems }
