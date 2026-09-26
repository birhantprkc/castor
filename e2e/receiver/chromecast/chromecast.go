// Package chromecast is a Cast receiver on TLS: it launches the Default Media Receiver and plays what LOAD names.
package chromecast

import (
	"crypto/tls"
	"fmt"
	"net"
	"slices"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/receiver"
	"github.com/stupside/castor/e2e/strategy"
)

// plays is what the Default Media Receiver decodes, as castor's Cast family states it.
var plays = receiver.Plays{
	Video:  map[string]int{"h264": 8, "vp8": 8},
	Audio:  map[string]int{"aac": 6, "ac3": 0, "eac3": 0, "mp3": 0, "vorbis": 0},
	Levels: map[string]int{"h264": 42},
}

const (
	nsReceiver       = "urn:x-cast:com.google.cast.receiver"
	nsMedia          = "urn:x-cast:com.google.cast.media"
	defaultMediaApp  = "CC1AD845"
	mediaTransportID = "transport-1"
	mediaSessionID   = 1
)

// idleReasons is the Cast word for how an over playback went idle.
var idleReasons = map[receiver.Playback]string{
	receiver.Ended:   "FINISHED",
	receiver.Stopped: "CANCELLED",
}

// Settings are how strict the receiver is; castor states the same envelope for every model.
type Settings struct {
	CORS CORS `yaml:"cors"`
}

// Family builds a Cast receiver, as in `device: {chromecast: {cors: enforced}}`.
type Family struct{}

func (Family) Name() string { return "chromecast" }

func (Family) Build(raw yaml.Node) (receiver.Device, error) {
	settings := Settings{CORS: Ignored}
	if err := strategy.Decode(raw, &settings); err != nil {
		return nil, fmt.Errorf("chromecast: %w", err)
	}
	if !slices.Contains([]CORS{Ignored, Enforced}, settings.CORS) {
		return nil, fmt.Errorf("chromecast: cors %q: want %s or %s", settings.CORS, Ignored, Enforced)
	}
	return device{settings}, nil
}

type device struct{ Settings }

func (device) Type() string { return "chromecast" }

func (d device) Start(t *testing.T, s *receiver.Session) (receiver.Endpoint, error) {
	cert, err := selfSigned()
	if err != nil {
		return receiver.Endpoint{}, err
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		return receiver.Endpoint{}, fmt.Errorf("listening for cast senders: %w", err)
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		conns []net.Conn
	)
	wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			wg.Go(func() { (&castSession{session: s, conn: conn, cors: d.CORS}).serve() })
		}
	})
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return receiver.Endpoint{Host: ln.Addr().String(), Plays: plays, EndsWithin: receiver.Reported}, nil
}
