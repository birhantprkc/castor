package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// relay serves the client's renderers on the LAN what the server serves, so a renderer never reaches the server itself.
type relay struct {
	base   *url.URL
	server *http.Server
}

func openRelay(ctx context.Context, server string, addrs Addresses) (*relay, error) {
	upstream, err := url.Parse(server)
	if err != nil {
		return nil, fmt.Errorf("relay upstream %q: %w", server, err)
	}
	ip, err := addrs.LocalIPv4(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving the address renderers reach this client at: %w", err)
	}
	l, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		return nil, fmt.Errorf("opening the relay: %w", err)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.Host = upstream.Host
		},
		// Streams flow as they are written: a renderer starved behind a buffering proxy reads as a stall.
		FlushInterval: -1,
	}
	r := &relay{
		base:   &url.URL{Scheme: "http", Host: l.Addr().String(), Path: "/"},
		server: &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second},
	}
	go func() { _ = r.server.Serve(l) }()
	return r, nil
}

// url is where a renderer fetches the server's relay path through this client.
func (r *relay) url(path string) (*url.URL, error) {
	rel, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("relay path %q: %w", path, err)
	}
	return r.base.ResolveReference(rel), nil
}

func (r *relay) close() { _ = r.server.Close() }
