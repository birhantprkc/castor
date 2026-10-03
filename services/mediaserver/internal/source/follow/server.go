package follow

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// server republishes each feed on loopback: its playlist at /name.m3u8, and every resource it lists under /name/.
type server struct {
	http *http.Server
	base *url.URL
}

// serve starts republishing feeds, each under its name.
func serve(feeds ...*feed) (*server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening for the republished timeline: %w", err)
	}
	mux := http.NewServeMux()
	for _, feed := range feeds {
		mux.HandleFunc("GET /"+feed.name+".m3u8", feed.playlist)
		mux.HandleFunc("GET /"+feed.name+"/{sequence}", feed.segment)
		mux.HandleFunc("GET /"+feed.name+"/init/{id}", feed.init)
		mux.HandleFunc("GET /"+feed.name+"/key/{id}", feed.key)
	}
	s := &server{
		http: &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second},
		base: &url.URL{Scheme: "http", Host: ln.Addr().String()},
	}
	go func() { _ = s.http.Serve(ln) }()
	return s, nil
}

// URL is where the playlist of the feed served under name is read.
func (s *server) URL(name string) *url.URL { return s.base.JoinPath(name + ".m3u8") }

func (s *server) Close() error { return s.http.Close() }
