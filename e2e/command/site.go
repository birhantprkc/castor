package command

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stupside/castor/e2e/judge"
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
	quoted, _ := json.Marshal(url)
	return "fetch(" + string(quoted) + `, {mode: "no-cors"}).catch(function () {});`
}

var document = template.Must(template.New("page").Parse(`<!doctype html>
<html><head><title>castor e2e</title></head>
<body><video autoplay muted></video>
{{.Markup}}
<script>{{range .Scripts}}{{.}}
{{end}}</script>
</body></html>`))

// site serves the page on every route it does not mount a decoy on, and records which routes a browser asked for.
type site struct {
	url   string
	mu    sync.Mutex
	paths []string
}

func serveSite(t *testing.T, src *origin.Origin, p Page) *site {
	s := &site{}
	mux := http.NewServeMux()
	markup, script := p.Offer.Mount(t, mux, src)
	scripts := []template.JS{script}
	for _, d := range p.Decoys {
		scripts = append(scripts, template.JS(d.Mount(mux, src)))
	}
	page := struct {
		Markup  template.HTML
		Scripts []template.JS
	}{markup, scripts}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = document.Execute(w, page)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	s.url = server.URL
	return s
}

func (s *site) visited(route string) judge.Check { return visited{site: s, route: route} }

// visited holds that castor's browser opened the page at the route the command names.
type visited struct {
	site  *site
	route string
}

func (visited) Name() string { return "page" }

func (v visited) Judge(judge.Evidence) []string {
	v.site.mu.Lock()
	defer v.site.mu.Unlock()
	if slices.Contains(v.site.paths, v.route) {
		return nil
	}
	return []string{fmt.Sprintf("the browser opened %v, want %s", strings.Join(v.site.paths, ", "), v.route)}
}
