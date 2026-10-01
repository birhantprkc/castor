package server

import (
	"net/http"
	"net/http/httputil"
)

// relayRoute is where a client fetches what a cast serves: only the ports that cast handed its renderer.
const relayRoute = "/relay/{cast}/{port}/{path...}"

// relay proxies a client's fetch to what its cast serves on loopback.
type relay struct {
	registry *registry
	proxy    *httputil.ReverseProxy
}

func newRelay(registry *registry) relay {
	return relay{registry: registry, proxy: &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", loopback(pr.In.PathValue("port"))
			pr.Out.URL.Path, pr.Out.URL.RawPath = "/"+pr.In.PathValue("path"), ""
			pr.Out.Host = pr.Out.URL.Host
		},
		// Streams flow as they are written: a renderer starved behind a buffering proxy reads as a stall.
		FlushInterval: -1,
	}}
}

func (rl relay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s, err := rl.registry.find(r.PathValue("cast"))
	if err != nil || !s.relays.serves(r.PathValue("port")) {
		http.NotFound(w, r)
		return
	}
	rl.proxy.ServeHTTP(w, r)
}
