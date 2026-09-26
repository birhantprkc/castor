package command

import (
	"testing"

	"github.com/stupside/castor/e2e/origin"
)

// URL casts the stream's own link, as `castor cast url` does.
type URL struct{}

func (URL) Name() string { return "url" }

func (URL) Invoke(_ *testing.T, src *origin.Origin) Invocation {
	return Invocation{Args: []string{"cast", "url", src.URL}}
}
