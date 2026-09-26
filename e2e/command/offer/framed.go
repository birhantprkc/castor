package offer

import (
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/command"
	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// Loading is the iframe's loading attribute: a lazy frame far below the fold loads only once scrolled to.
type Loading string

const (
	Eager Loading = "eager"
	Lazy  Loading = "lazy"
)

// Framed embeds the player in an iframe from an embed host that serves only pages framed by a site,
// as in `stream: {framed: {loading: lazy}}`.
type Framed struct{}

func (Framed) Name() string { return "framed" }

func (Framed) Build(raw yaml.Node) (command.Offer, error) {
	var settings struct {
		Loading Loading `yaml:"loading"`
	}
	if err := strategy.Decode(raw, &settings); err != nil {
		return nil, fmt.Errorf("framed: %w", err)
	}
	if !slices.Contains([]Loading{Eager, Lazy}, settings.Loading) {
		return nil, fmt.Errorf("framed: loading %q: want %s or %s", settings.Loading, Eager, Lazy)
	}
	return framed{loading: settings.Loading}, nil
}

type framed struct{ loading Loading }

func (framed) Name() string { return "framed" }

func (f framed) Mount(t *testing.T, _ *http.ServeMux, src *origin.Origin) (template.HTML, template.JS) {
	embed := http.NewServeMux()
	embed.HandleFunc("GET /embed/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Referer() == "" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "This video can only be watched on its site")
			return
		}
		fmt.Fprintf(w, "<!doctype html>\n<html><body><video autoplay muted></video>\n<script>%s</script>\n</body></html>", command.Fetch(src.URL))
	})
	server := httptest.NewServer(embed)
	t.Cleanup(server.Close)
	// localhost is another site than the page's 127.0.0.1, so the frame is a third-party embed.
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	return template.HTML(fmt.Sprintf(
		`<div style="height:10000px">article</div><iframe loading="%s" width="800" height="450" src="http://localhost:%s/embed/1"></iframe>`,
		f.loading, port)), ""
}
