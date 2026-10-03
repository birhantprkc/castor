package dlna

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/huin/goupnp"
	"github.com/huin/goupnp/soap"

	"github.com/stupside/castor/services/apiserver/internal/device"
	"github.com/stupside/castor/services/apiserver/internal/device/devicetest"
)

func TestTheTransportStateEndsTheCastOnlyOncePlaybackBegan(t *testing.T) {
	for _, tt := range []struct {
		name   string
		states []string
		want   bool
	}{
		{"stop before playback is ignored", []string{"STOPPED", "TRANSITIONING", "STOPPED"}, false},
		{"stop after playing ends", []string{"PLAYING", "STOPPED"}, true},
		{"pause remains active", []string{"PLAYING", "PAUSED_PLAYBACK"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var watch transportWatch
			got := false
			for _, state := range tt.states {
				got = watch.observe(state)
			}
			if got != tt.want {
				t.Errorf("observe(%v) = %v, want %v", tt.states, got, tt.want)
			}
		})
	}
}

func TestAnUnbrokenRunOfFailedPollsIsNeededToEndTheCast(t *testing.T) {
	type answer = struct {
		state string
		err   error
	}
	gone := errors.New("connect: connection refused")
	var script []answer
	for range 2 {
		for range int(device.UnreachableWindow/device.PollInterval) - 1 {
			script = append(script, answer{err: gone})
		}
		script = append(script, answer{state: "PLAYING"})
	}
	script[len(script)-1].state = "STOPPED"

	synctest.Test(t, func(t *testing.T) {
		calls := 0
		poll := func(context.Context) (string, error) {
			a := script[min(calls, len(script)-1)]
			calls++
			return a.state, a.err
		}
		if err := awaitTransportEnd(t.Context(), "Living Room", poll); err != nil {
			t.Fatalf("awaitTransportEnd() = %v, want nil: the device answered between the failures", err)
		}
		if calls != len(script) {
			t.Errorf("polled %d times, want %d", calls, len(script))
		}
	})
}

// playingDevice answers every GetTransportInfo with PLAYING, counting the polls.
type playingDevice struct{ polls atomic.Int32 }

func (r *playingDevice) RoundTrip(*http.Request) (*http.Response, error) {
	r.polls.Add(1)
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` +
		`<u:GetTransportInfoResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">` +
		`<CurrentTransportState>PLAYING</CurrentTransportState><CurrentTransportStatus>OK</CurrentTransportStatus><CurrentSpeed>1</CurrentSpeed>` +
		`</u:GetTransportInfoResponse></s:Body></s:Envelope>`
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}, nil
}

func TestDLNAAnswersWhenTheCastEnds(t *testing.T) {
	dev := &playingDevice{}
	devicetest.AwaitsTheCastsEnd(t, func() device.Device {
		client := soap.NewSOAPClient(url.URL{Scheme: "http", Host: "192.0.2.10:2870", Path: "/control"})
		client.HTTPClient.Transport = dev
		return &session{transport: goupnp.ServiceClient{
			SOAPClient: client,
			Service:    &goupnp.Service{ServiceType: "urn:schemas-upnp-org:service:AVTransport:1"},
		}}
	})
	if dev.polls.Load() == 0 {
		t.Error("the suite never polled the device")
	}
}
