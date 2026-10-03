package command

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"html/template"
	"net/http"
	"testing"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// Decoy is something a page requests beside its stream, as real pages load ads, posters and player internals.
type Decoy interface {
	strategy.Named
	// Mount serves the decoy on the page's site and returns the script statement that requests it.
	Mount(mux *http.ServeMux, src *origin.Origin) string
}

// Offer is how a page hands its player the stream, if it does at all.
type Offer interface {
	strategy.Named
	// Mount serves what the offer needs beside the page and returns the markup placed in it and the script it runs.
	Mount(t *testing.T, mux *http.ServeMux, src *origin.Origin) (template.HTML, template.JS)
}

// Page is what a site's page requests from script when a browser opens it.
type Page struct {
	Offer  Offer
	Decoys []Decoy
}

// Fetch is the script statement requesting url the way a media element does: no-cors, with the page's Referer.
func Fetch(url string) string {
	return "fetch(" + Quote(url) + `, {mode: "no-cors"}).catch(function () {});`
}

// Quote is s as a JavaScript string literal that cannot close the script element it sits in.
func Quote(s string) string {
	quoted, _ := json.Marshal(s, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true))
	return string(quoted)
}

var document = template.Must(template.New("page").Parse(`<!doctype html>
<html><head><title>castor e2e</title></head>
<body><video autoplay muted></video>
{{.Markup}}
<script>{{range .Scripts}}{{.}}
{{end}}</script>
</body></html>`))
