package expect

import (
	"fmt"

	"github.com/stupside/castor/e2e/judge"
)

// Passthrough holds that the receiver was handed a URL on the origin, the source or one of its renditions, and fetched it itself.
type Passthrough struct{}

func (Passthrough) Name() string { return "passthrough" }

func (Passthrough) Judge(e judge.Evidence) []string {
	return judge.FailIf(!e.HandedOrigin() || !e.ReceiverFetchedOrigin(),
		fmt.Sprintf("handed %s, want a URL on the origin fetched by the receiver itself", e.Received.URL))
}
