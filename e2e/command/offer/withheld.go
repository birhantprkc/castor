// Package offer is how a page hands its player the stream: late, only in its console, inside a frame, or not at all.
package offer

import (
	"html/template"
	"net/http"
	"testing"

	"github.com/stupside/castor/e2e/origin"
)

// Withheld never requests the stream, as a page whose player castor cannot see (YouTube) or that serves only decoys.
type Withheld struct{}

func (Withheld) Name() string { return "withheld" }

func (Withheld) Mount(*testing.T, *http.ServeMux, *origin.Origin) (template.HTML, template.JS) {
	return "", ""
}
