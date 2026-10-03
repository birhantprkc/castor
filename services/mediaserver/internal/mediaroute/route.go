// Package mediaroute is where devices fetch what casts serve: one route, without credentials, to the loopback listeners a cast opened.
package mediaroute

import (
	"net/http"
	"net/http/httputil"
)

// Pattern is the route devices fetch what casts serve from.
const Pattern = "/media/{cast}/{port}/{path...}"

// Handler proxies a device's fetch to the loopback port it names, when serves says that cast serves it.
func Handler(serves func(cast, port string) bool) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", loopback(pr.In.PathValue("port"))
			pr.Out.URL.Path, pr.Out.URL.RawPath = "/"+pr.In.PathValue("path"), ""
			pr.Out.Host = pr.Out.URL.Host
		},
		// Streams flow as they are written: a device starved behind a buffering proxy reads as a stall.
		FlushInterval: -1,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !serves(r.PathValue("cast"), r.PathValue("port")) {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}
