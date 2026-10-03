package dlna

import (
	"io"
	"net/http"

	"github.com/stupside/castor/e2e/receiver"
)

// Fetches is how the device's HTTP stack opens the media Play names.
type Fetches string

const (
	// Once opens it with the one fetch it plays.
	Once Fetches = "once"
	// HeadProbeReplay sends HEAD, reads a probe's worth and hangs up, then reopens it with Range: bytes=0-, as many devices do.
	HeadProbeReplay Fetches = "head-probe-replay"
)

// probeBytes is what a device reads to sniff the container before it hangs up.
const probeBytes = 64 << 10

// fetchers start the playback of uri, declared as the DIDL's content type.
var fetchers = map[Fetches]func(s *receiver.Session, uri, declared string){
	Once:            func(s *receiver.Session, uri, declared string) { s.Hand(uri, declared) },
	HeadProbeReplay: headProbeReplay,
}

// headProbeReplay probes after Play is answered, as a device does rather than hold its SOAP reply past the sender's timeout.
func headProbeReplay(s *receiver.Session, uri, declared string) {
	s.Go(func() {
		if resp, ok := probe(s, http.MethodHead, uri); ok {
			resp.Body.Close()
			for _, v := range streamHeaders(declared, resp.Header) {
				s.Problem("HEAD %s: %s", uri, v)
			}
		}
		if resp, ok := probe(s, http.MethodGet, uri); ok {
			if _, err := io.ReadFull(resp.Body, make([]byte, probeBytes)); err != nil {
				s.Problem("probing %s: %v", uri, err)
			}
			resp.Body.Close()
		}
		s.Header("Range", "bytes=0-")
		s.Header("getcontentFeatures.dlna.org", "1")
		s.Hand(uri, declared)
	})
}

// probe sends one of the device's requests before playback, reporting any answer but 200.
func probe(s *receiver.Session, method, uri string) (*http.Response, bool) {
	req, err := http.NewRequestWithContext(s.Context(), method, uri, nil)
	if err != nil {
		s.Problem("%s %s: %v", method, uri, err)
		return nil, false
	}
	req.Header.Set("User-Agent", receiver.UserAgent)
	req.Header.Set("getcontentFeatures.dlna.org", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.Problem("%s %s: %v", method, uri, err)
		return nil, false
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		s.Problem("%s %s: %s", method, uri, resp.Status)
		return nil, false
	}
	return resp, true
}
