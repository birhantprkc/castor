// Package settings is how castor's config reaches its process: a file it is pointed at, or its environment alone.
package settings

import (
	"testing"

	"github.com/stupside/castor/e2e/strategy"
)

// Carrier delivers a config document to castor.
type Carrier interface {
	strategy.Named
	Carry(t *testing.T, doc map[string]any) (Launch, error)
}

// Launch is how castor's process starts: flags before the command, extra environment, and working directory.
// The directory is always an empty one, so no stray config.yaml or config.local.yaml is read by accident.
type Launch struct {
	Flags []string
	Env   []string
	Dir   string
}
