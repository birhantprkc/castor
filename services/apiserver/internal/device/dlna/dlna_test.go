package dlna

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/huin/goupnp"
	"github.com/huin/goupnp/soap"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
	"github.com/stupside/castor/services/apiserver/internal/device"
	"github.com/stupside/castor/services/apiserver/internal/device/devicetest"
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
	if got := videoCodecs(caps); !slices.Equal(got, []mediav1.Codec{mediav1.Codec_CODEC_H264}) {
		t.Errorf("AVC-only sink video = %v, want H.264 and no HEVC", got)
	}
	if got := audioCodecs(caps); !slices.Equal(got, []mediav1.Codec{mediav1.Codec_CODEC_AAC, mediav1.Codec_CODEC_AC3}) {
		t.Errorf("sink audio = %v, want AAC and AC-3 from the AV profiles, no E-AC-3", got)
	}
	if got := caps.GetContainers(); !slices.Equal(got, []mediav1.Container{mediav1.Container_CONTAINER_MPEGTS, mediav1.Container_CONTAINER_MP4}) {
		t.Errorf("sink containers = %v, want MPEG-TS and MP4", got)
	}
	if got := videoCodecs(parseSinkProtocolInfo(avcSink + ",http-get:*:video/mp2t:DLNA.ORG_PN=HEVC_TS_MAIN_HD")); !slices.Contains(got, mediav1.Codec_CODEC_HEVC) {
		t.Errorf("HEVC_TS sink video = %v, want HEVC advertised", got)
	}
	// No video codec makes negotiateCaps substitute fallbackCaps.
	if got := parseSinkProtocolInfo("garbage,http-get:*:audio/mpeg:*"); len(got.Video) != 0 {
		t.Errorf("unusable sink should yield no video, got %v", got.Video)
	}
}

func videoCodecs(caps *mediav1.Capabilities) []mediav1.Codec {
	var out []mediav1.Codec
	for _, v := range caps.GetVideo() {
		out = append(out, v.GetCodec())
	}
	return out
}

func audioCodecs(caps *mediav1.Capabilities) []mediav1.Codec {
	var out []mediav1.Codec
	for _, a := range caps.GetAudio() {
		out = append(out, a.GetCodec())
	}
	return out
}

func TestARendererIsHandedEachContainerUnderItsDLNAProfile(t *testing.T) {
	stream := &url.URL{Scheme: "http", Host: "192.0.2.1:8080", Path: "/stream"}
	for container, want := range map[mediav1.Container]string{
		mediav1.Container_CONTAINER_MPEGTS: "http-get:*:video/mp2t:DLNA.ORG_PN=MPEG_TS_HD_NA_ISO;DLNA.ORG_OP=00;DLNA.ORG_CI=1;DLNA.ORG_FLAGS=8D300000000000000000000000000000",
		mediav1.Container_CONTAINER_MP4:    "http-get:*:video/mp4:DLNA.ORG_PN=AVC_MP4_HP_HD_AAC;DLNA.ORG_OP=00;DLNA.ORG_CI=1;DLNA.ORG_FLAGS=01300000000000000000000000000000",
	} {
		metadata, err := buildDIDLMetadata(stream, container)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(metadata, `protocolInfo="`+want+`"`) {
			t.Errorf("%v was announced as %s, want protocolInfo %q", container, metadata, want)
		}
		if got := servedHeaders(container)["contentFeatures.dlna.org"]; !strings.HasSuffix(want, got) {
			t.Errorf("%v is served with contentFeatures %q, want the features it was announced with", container, got)
		}
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
