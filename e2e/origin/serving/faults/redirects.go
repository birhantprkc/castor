package faults

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// Redirects builds a CDN director: every link redirects through hops to a fresh edge, as in `redirects: {hops: 2, uses: 1}`.
type Redirects struct{}

func (Redirects) Name() string { return "redirects" }

func (Redirects) Build(settings yaml.Node) (origin.Behaviour, error) {
	r := director{Hops: 1}
	if err := strategy.Decode(settings, &r); err != nil {
		return nil, fmt.Errorf("redirects: %w", err)
	}
	if r.Hops < 1 || r.Uses < 0 {
		return nil, errors.New("redirects: want hops >= 1, and uses >= 0 where 0 is unlimited")
	}
	return &r, nil
}

type director struct {
	Hops int `yaml:"hops"`
	// Uses is how often each document under one edge token is served; segments are never limited.
	Uses int `yaml:"uses"`

	mu    sync.Mutex
	edges int
	used  map[string]int
}

func (*director) Name() string { return "redirects" }

func (d *director) Wrap(next http.Handler, p origin.Published) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, "/hop/"); ok {
			k, after, _ := strings.Cut(rest, "/")
			if n, err := strconv.Atoi(k); err == nil && n+1 < d.Hops {
				d.redirect(w, r, fmt.Sprintf("/hop/%d/%s", n+1, after), http.StatusTemporaryRedirect)
				return
			}
			d.redirect(w, r, d.edge(after), http.StatusTemporaryRedirect)
			return
		}
		if rest, ok := strings.CutPrefix(r.URL.Path, "/edge/"); ok {
			token, after, _ := strings.Cut(rest, "/")
			// A clone, so the origin still records the edge path it was asked for.
			served := r.Clone(r.Context())
			served.URL.Path, served.URL.RawPath = "/"+after, ""
			if !p.IsSegment(served) && d.spent(token+"/"+after) {
				http.Error(w, "token spent", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, served)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/")
		if d.Hops == 1 {
			d.redirect(w, r, d.edge(rest), http.StatusFound)
			return
		}
		d.redirect(w, r, "/hop/1/"+rest, http.StatusFound)
	})
}

// edge mints a fresh edge token for a published path.
func (d *director) edge(rest string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.edges++
	return fmt.Sprintf("/edge/%d/%s", d.edges, rest)
}

// spent counts a use of one document under one token, and reports whether it is past its uses.
func (d *director) spent(document string) bool {
	if d.Uses == 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.used == nil {
		d.used = map[string]int{}
	}
	d.used[document]++
	return d.used[document] > d.Uses
}

// redirect points at a path on this host, keeping the query a signed link carries.
func (*director) redirect(w http.ResponseWriter, r *http.Request, to string, status int) {
	if r.URL.RawQuery != "" {
		to += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, to, status)
}
