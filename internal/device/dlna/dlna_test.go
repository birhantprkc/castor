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

	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/device/devicetest"
	"github.com/stupside/castor/internal/media"
)

func TestDLNATransportState(t *testing.T) {
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

func TestFindServiceReachesAVersion3OnlyRenderer(t *testing.T) {
	service := func(serviceType string) goupnp.Service {
		return goupnp.Service{
			ServiceType: serviceType,
			ControlURL:  goupnp.URLField{URL: url.URL{Scheme: "http", Host: "192.0.2.10:2870", Path: "/control"}, Ok: true},
		}
	}
	const v3 = "urn:schemas-upnp-org:service:AVTransport:3"
	loc := &url.URL{Scheme: "http", Host: "192.0.2.10:2870", Path: "/dmr.xml"}

	// Philips 50PUD6654/43 and similar sets publish the v3 service only.
	root := &goupnp.RootDevice{Device: goupnp.Device{Services: []goupnp.Service{service(v3)}}}
	got, err := findService(root, loc, "AVTransport")
	if err != nil || got.Service.ServiceType != v3 {
		t.Fatalf("findService() = %q, %v, want %q", got.Service.ServiceType, err, v3)
	}

	bare := &goupnp.RootDevice{Device: goupnp.Device{Services: []goupnp.Service{service("urn:schemas-upnp-org:service:RenderingControl:3")}}}
	if _, err := findService(bare, loc, "AVTransport"); err == nil {
		t.Error("a renderer without any AVTransport was accepted")
	}
}

func TestParseSinkProtocolInfo(t *testing.T) {
	avcSink := "http-get:*:audio/mpeg:*," +
		"http-get:*:video/mp2t:DLNA.ORG_PN=AVC_TS_HD_50_AC3_ISO," +
		"http-get:*:video/mp4:DLNA.ORG_PN=AVC_MP4_MP_HD_AAC"

	caps := parseSinkProtocolInfo(avcSink)
	if !caps.SupportsCodec(media.CodecH264) || caps.SupportsCodec(media.CodecHEVC) {
		t.Errorf("AVC-only sink video = %v, want H.264 and no HEVC", caps.Video)
	}
	if !caps.SupportsAudioCodec(media.CodecAAC) || !caps.SupportsAudioCodec(media.CodecAC3) || caps.SupportsAudioCodec(media.CodecEAC3) {
		t.Errorf("sink audio = %v, want AAC and AC-3 from the AV profiles, no E-AC-3", caps.Audio)
	}
	if !parseSinkProtocolInfo(avcSink + ",http-get:*:video/mp2t:DLNA.ORG_PN=HEVC_TS_MAIN_HD").SupportsCodec(media.CodecHEVC) {
		t.Error("HEVC_TS sink should advertise HEVC")
	}
	// No video codec makes negotiateCaps substitute fallbackCaps.
	if got := parseSinkProtocolInfo("garbage,http-get:*:audio/mpeg:*"); len(got.Video) != 0 {
		t.Errorf("unusable sink should yield no video, got %v", got.Video)
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
		for range device.UnreachablePolls - 1 {
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
		if err := awaitTransportEnd(t.Context(), "Living Room TV", device.UnreachablePolls, device.PollInterval, poll); err != nil {
			t.Fatalf("awaitTransportEnd() = %v, want nil: the renderer answered between the failures", err)
		}
		if calls != len(script) {
			t.Errorf("polled %d times, want %d", calls, len(script))
		}
	})
}

// playingRenderer answers every GetTransportInfo with PLAYING, counting the polls.
type playingRenderer struct{ polls atomic.Int32 }

func (r *playingRenderer) RoundTrip(*http.Request) (*http.Response, error) {
	r.polls.Add(1)
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` +
		`<u:GetTransportInfoResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">` +
		`<CurrentTransportState>PLAYING</CurrentTransportState><CurrentTransportStatus>OK</CurrentTransportStatus><CurrentSpeed>1</CurrentSpeed>` +
		`</u:GetTransportInfoResponse></s:Body></s:Envelope>`
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}, nil
}

func TestDLNAAnswersWhenTheCastEnds(t *testing.T) {
	renderer := &playingRenderer{}
	devicetest.AwaitsTheCastsEnd(t, func() device.Device {
		client := soap.NewSOAPClient(url.URL{Scheme: "http", Host: "192.0.2.10:2870", Path: "/control"})
		client.HTTPClient.Transport = renderer
		return &dlnaDevice{transport: goupnp.ServiceClient{
			SOAPClient: client,
			Service:    &goupnp.Service{ServiceType: "urn:schemas-upnp-org:service:AVTransport:1"},
		}}
	})
	if renderer.polls.Load() == 0 {
		t.Error("the suite never polled the renderer")
	}
}

func TestTheFallbackDeclaresTheUniversalBaseline(t *testing.T) {
	devicetest.DeclaresTheUniversalBaseline(t, fallbackCaps())
}
