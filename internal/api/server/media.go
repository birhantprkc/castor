package server

import (
	"net/http"
	"net/http/httputil"
)

const mediaPattern = "/media/{cast}/{port}/{path...}"

type mediaRoute struct {
	registry *registry
	proxy    *httputil.ReverseProxy
}

func newMediaRoute(registry *registry) mediaRoute {
	return mediaRoute{registry: registry, proxy: &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", loopback(pr.In.PathValue("port"))
			pr.Out.URL.Path, pr.Out.URL.RawPath = "/"+pr.In.PathValue("path"), ""
			pr.Out.Host = pr.Out.URL.Host
		},
		// Streams flow as they are written: a renderer starved behind a buffering proxy reads as a stall.
		FlushInterval: -1,
	}}
}

func (m mediaRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s, err := m.registry.find(r.PathValue("cast"))
	if err != nil || !s.deliveries.serves(r.PathValue("port")) {
		http.NotFound(w, r)
		return
	}
	m.proxy.ServeHTTP(w, r)
}
