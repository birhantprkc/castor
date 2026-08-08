package device

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/huin/goupnp"
	"github.com/huin/goupnp/soap"

	"github.com/stupside/castor/internal/media"
)

func TestFindService(t *testing.T) {
	service := func(serviceType string) goupnp.Service {
		return goupnp.Service{
			ServiceType: serviceType,
			ControlURL:  goupnp.URLField{URL: url.URL{Scheme: "http", Host: "192.0.2.10:2870", Path: "/control/" + serviceType}, Ok: true},
		}
	}
	root := func(services ...goupnp.Service) *goupnp.RootDevice {
		return &goupnp.RootDevice{Device: goupnp.Device{
			FriendlyName: "Test Renderer",
			UDN:          "uuid:test",
			Services:     services,
		}}
	}
	const (
		v1 = "urn:schemas-upnp-org:service:AVTransport:1"
		v2 = "urn:schemas-upnp-org:service:AVTransport:2"
		v3 = "urn:schemas-upnp-org:service:AVTransport:3"
		rc = "urn:schemas-upnp-org:service:RenderingControl:3"
		cm = "urn:schemas-upnp-org:service:ConnectionManager:3"
	)

	tests := []struct {
		name    string
		root    *goupnp.RootDevice
		service string
		want    string
	}{
		{
			name:    "version 1 service is driven as before",
			root:    root(service(v1)),
			service: "AVTransport",
			want:    v1,
		},
		{
			// Philips 50PUD6654/43 and similar sets publish the v3 service only.
			name:    "version 3 only renderer is reachable",
			root:    root(service(rc), service(cm), service(v3)),
			service: "AVTransport",
			want:    v3,
		},
		{
			name:    "version 2 only renderer is reachable",
			root:    root(service(v2)),
			service: "AVTransport",
			want:    v2,
		},
		{
			name:    "newest published version wins",
			root:    root(service(v1), service(v2), service(v3)),
			service: "AVTransport",
			want:    v3,
		},
		{
			name: "service nested in a sub-device is found",
			root: &goupnp.RootDevice{Device: goupnp.Device{
				FriendlyName: "Test Renderer",
				Devices:      []goupnp.Device{{Services: []goupnp.Service{service(v3)}}},
			}},
			service: "AVTransport",
			want:    v3,
		},
		{
			name:    "renderer without any AVTransport is rejected",
			root:    root(service(rc), service(cm)),
			service: "AVTransport",
			want:    "",
		},
		{
			// Capability negotiation is version-locked the same way: a v3-only
			// ConnectionManager would otherwise degrade to fallbackCaps and
			// re-encode a stream the renderer could have copied.
			name:    "version 3 ConnectionManager is reachable for negotiation",
			root:    root(service(rc), service(cm), service(v3)),
			service: "ConnectionManager",
			want:    cm,
		},
	}

	loc := &url.URL{Scheme: "http", Host: "192.0.2.10:2870", Path: "/dmr.xml"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findService(tt.root, loc, tt.service)
			if tt.want == "" {
				if err == nil {
					t.Fatalf("findService() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("findService() error = %v, want nil", err)
			}
			if got.Service.ServiceType != tt.want {
				t.Errorf("findService() service = %q, want %q", got.Service.ServiceType, tt.want)
			}
			// The SOAP action namespace must match the published version, or a
			// strict renderer rejects the call as an invalid action.
			if got.SOAPClient == nil {
				t.Error("findService() returned a client with no SOAPClient")
			}
		})
	}
}

func TestDLNAInfo(t *testing.T) {
	rootDevice := func(name string) *goupnp.RootDevice {
		return &goupnp.RootDevice{Device: goupnp.Device{FriendlyName: name}}
	}
	location := func(host, path string) *url.URL {
		return &url.URL{Scheme: "http", Host: host, Path: path}
	}

	tests := []struct {
		name   string
		result goupnp.MaybeRootDevice
		want   Info
		ok     bool
	}{
		{
			name:   "named renderer with a location",
			result: goupnp.MaybeRootDevice{Root: rootDevice("Living Room TV"), Location: location("192.0.2.10:8200", "/rootDesc.xml")},
			want:   Info{Name: "Living Room TV", Type: TypeDLNA, Address: "http://192.0.2.10:8200/rootDesc.xml"},
			ok:     true,
		},
		{
			name:   "second renderer maps independently",
			result: goupnp.MaybeRootDevice{Root: rootDevice("Bedroom Speaker"), Location: location("192.0.2.20:49152", "/desc.xml")},
			want:   Info{Name: "Bedroom Speaker", Type: TypeDLNA, Address: "http://192.0.2.20:49152/desc.xml"},
			ok:     true,
		},
		{
			name:   "announcement without a root device is rejected",
			result: goupnp.MaybeRootDevice{Location: location("192.0.2.30:8200", "/desc.xml")},
			ok:     false,
		},
		{
			name:   "announcement without a location is rejected",
			result: goupnp.MaybeRootDevice{Root: rootDevice("Ghost")},
			ok:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := dlnaInfo(tt.result)
			if ok != tt.ok {
				t.Fatalf("dlnaInfo() ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("dlnaInfo() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSinkProtocolInfo(t *testing.T) {
	// A representative Samsung-style Sink: many AVC/MPEG entries, no HEVC token.
	avcSink := "http-get:*:audio/mpeg:*," +
		"http-get:*:video/mp2t:DLNA.ORG_PN=AVC_TS_HD_50_AC3_ISO," +
		"http-get:*:video/mp4:DLNA.ORG_PN=AVC_MP4_MP_HD_AAC," +
		"http-get:*:video/x-msvideo:*"

	caps := parseSinkProtocolInfo(avcSink)
	if !caps.SupportsCodec(media.CodecH264) {
		t.Error("AVC sink should advertise H.264")
	}
	if caps.SupportsCodec(media.CodecHEVC) {
		t.Error("AVC-only sink must not advertise HEVC")
	}
	if !slices.Contains(caps.Containers, media.MPEGTS) {
		t.Errorf("expected %q container, got %v", media.MPEGTS, caps.Containers)
	}

	// The H.264 envelope copies a codec-safe stream, not a 10-bit one.
	safe := media.ProbeInfo{VideoCodec: media.CodecH264, VideoProfile: "High", VideoBitDepth: 8}
	if !caps.CanCopyVideo(safe) {
		t.Error("codec-safe High H.264 should be copy-eligible")
	}
	tenBit := safe
	tenBit.VideoBitDepth = 10
	if caps.CanCopyVideo(tenBit) {
		t.Error("10-bit H.264 must not be copy-eligible")
	}

	// Audio codecs ride the combined AV profile tokens: AVC_MP4_..._AAC lights up
	// AAC, AVC_TS_HD_50_AC3_ISO lights up AC-3; nothing carries E-AC-3.
	if !caps.SupportsAudioCodec(media.CodecAAC) {
		t.Error("an AAC profile should advertise AAC audio")
	}
	if !caps.SupportsAudioCodec(media.CodecAC3) {
		t.Error("an AC3 TS profile should advertise AC-3 audio")
	}
	if caps.SupportsAudioCodec(media.CodecEAC3) {
		t.Error("no E-AC-3 token present; must not advertise E-AC-3")
	}

	// AAC is trusted stereo-only: a stereo track copies, a 5.1 track does not.
	if !caps.CanCopyAudio(media.ProbeInfo{AudioCodec: media.CodecAAC, AudioChannels: 2}) {
		t.Error("stereo AAC should be copy-eligible")
	}
	if caps.CanCopyAudio(media.ProbeInfo{AudioCodec: media.CodecAAC, AudioChannels: 6}) {
		t.Error("5.1 AAC must not copy to a stereo-AAC renderer")
	}
	// AC-3 is inherently surround: a 5.1 track copies intact.
	if !caps.CanCopyAudio(media.ProbeInfo{AudioCodec: media.CodecAC3, AudioChannels: 6}) {
		t.Error("5.1 AC-3 should be copy-eligible on an AC-3 renderer")
	}

	// A renderer advertising an HEVC TS profile lights up HEVC.
	hevcSink := avcSink + ",http-get:*:video/mp2t:DLNA.ORG_PN=HEVC_TS_MAIN_HD"
	if !parseSinkProtocolInfo(hevcSink).SupportsCodec(media.CodecHEVC) {
		t.Error("HEVC_TS sink should advertise HEVC")
	}

	// An explicit E-AC-3 audio entry is detected and not misread as AC-3.
	eac3Sink := avcSink + ",http-get:*:audio/eac3:DLNA.ORG_PN=EAC3"
	if !parseSinkProtocolInfo(eac3Sink).SupportsAudioCodec(media.CodecEAC3) {
		t.Error("an EAC3 entry should advertise E-AC-3")
	}

	// Nothing usable yields no video codecs; the caller substitutes fallbackCaps.
	if got := parseSinkProtocolInfo("garbage,http-get:*:audio/mpeg:*"); len(got.Video) != 0 {
		t.Errorf("unusable sink should yield no video, got %v", got.Video)
	}
	if !fallbackCaps().SupportsCodec(media.CodecH264) {
		t.Error("fallbackCaps must at least support H.264")
	}

	// Every DLNA cast is served, so every DLNA capability record has to say what it
	// wants to be served, however the negotiation went. The executor takes the
	// container it produces from this field, and a renderer that declares none is
	// one castor has nothing to mux for.
	for _, tt := range []struct {
		name string
		caps media.Renderer
	}{
		{"negotiated", caps},
		{"degraded", parseSinkProtocolInfo("garbage")},
		{"fallback", fallbackCaps()},
	} {
		if got := tt.caps.ServedContainer; got != media.MPEGTS {
			t.Errorf("%s caps declare ServedContainer %q, want %q", tt.name, got, media.MPEGTS)
		}
	}
}

// transportLockedFault builds a SOAP fault carrying the given UPnP error code,
// as goupnp's PerformActionCtx returns it verbatim (unwrapped) on a fault.
func transportLockedFault(code int) error {
	fault := &soap.SOAPFaultError{}
	fault.Detail.UPnPError.Errorcode = code
	return fault
}

func TestIsTransportLocked(t *testing.T) {
	if !isTransportLocked(transportLockedFault(705)) {
		t.Error("error code 705 should be reported as transport locked")
	}
	if isTransportLocked(transportLockedFault(701)) {
		t.Error("a different UPnP error code must not be reported as transport locked")
	}
	if isTransportLocked(errors.New("some other error")) {
		t.Error("a non-SOAP-fault error must not be reported as transport locked")
	}
	if isTransportLocked(nil) {
		t.Error("a nil error must not be reported as transport locked")
	}
}

func TestRetryTransportLocked(t *testing.T) {
	t.Run("succeeds on first attempt without retrying", func(t *testing.T) {
		calls := 0
		err := retryTransportLocked(t.Context(), 5, time.Millisecond, func() error {
			calls++
			return nil
		})
		if err != nil {
			t.Fatalf("retryTransportLocked() error = %v, want nil", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})

	t.Run("retries while locked then succeeds", func(t *testing.T) {
		calls := 0
		err := retryTransportLocked(t.Context(), 5, time.Millisecond, func() error {
			calls++
			if calls < 3 {
				return transportLockedFault(705)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("retryTransportLocked() error = %v, want nil", err)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3", calls)
		}
	})

	t.Run("a non-lock error returns immediately without retrying", func(t *testing.T) {
		calls := 0
		wantErr := errors.New("boom")
		err := retryTransportLocked(t.Context(), 5, time.Millisecond, func() error {
			calls++
			return wantErr
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("retryTransportLocked() error = %v, want %v", err, wantErr)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1 (no retry)", calls)
		}
	})

	t.Run("exhausting retries returns the last lock error", func(t *testing.T) {
		calls := 0
		err := retryTransportLocked(t.Context(), 3, time.Millisecond, func() error {
			calls++
			return transportLockedFault(705)
		})
		if !isTransportLocked(err) {
			t.Fatalf("retryTransportLocked() error = %v, want a transport-locked error", err)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3", calls)
		}
	})

	t.Run("context cancellation stops retrying early", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		err := retryTransportLocked(ctx, 5, 50*time.Millisecond, func() error {
			calls++
			if calls == 1 {
				cancel()
			}
			return transportLockedFault(705)
		})
		if !isTransportLocked(err) {
			t.Fatalf("retryTransportLocked() error = %v, want a transport-locked error", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1 (cancellation should stop further retries)", calls)
		}
	})
}
