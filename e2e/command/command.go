// Package command is how castor is pointed at an origin: a direct link, a web page, or a title resolved through sources.
package command

import (
	"testing"

	"github.com/stupside/castor/e2e/judge"
	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// Command binds one way of invoking castor to a started origin.
type Command interface {
	strategy.Named
	Invoke(t *testing.T, src *origin.Origin) Invocation
}

// Invocation is castor's arguments after the config flag, the config it needs, and the checks only this command can answer.
type Invocation struct {
	Args   []string
	Config map[string]any
	Checks []judge.Check
}
