package command

import (
	"html/template"
	"net/http"
	"testing"

	"github.com/stupside/castor/e2e/origin"
)

// Fetched requests the stream as soon as the page runs, as a working player does and every title page does.
var Fetched Offer = fetched{}

type fetched struct{}

func (fetched) Name() string { return "fetched" }

func (fetched) Mount(_ *testing.T, _ *http.ServeMux, src *origin.Origin) (template.HTML, template.JS) {
	return "", template.JS(Fetch(src.URL))
}
