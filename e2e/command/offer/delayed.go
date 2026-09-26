package offer

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/command"
	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// Delayed requests the stream only after a pre-roll plays, as IMA/VAST setups do, as in `stream: {delayed: 30s}`.
type Delayed struct{}

func (Delayed) Name() string { return "delayed" }

func (Delayed) Build(settings yaml.Node) (command.Offer, error) {
	var after time.Duration
	if err := strategy.Decode(settings, &after); err != nil {
		return nil, fmt.Errorf("delayed: %w", err)
	}
	if after <= 0 {
		return nil, errors.New("delayed: want how long the page waits before it requests the stream, as 30s")
	}
	return delayed{after: after}, nil
}

type delayed struct{ after time.Duration }

func (delayed) Name() string { return "delayed" }

func (d delayed) Mount(_ *testing.T, _ *http.ServeMux, src *origin.Origin) (template.HTML, template.JS) {
	return "", template.JS(fmt.Sprintf("setTimeout(function () { %s }, %d);", command.Fetch(src.URL), d.after.Milliseconds()))
}
