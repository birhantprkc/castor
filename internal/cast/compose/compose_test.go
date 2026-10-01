package compose

import (
	"net/url"
	"testing"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// composed runs the profile pass, then the negotiated one, and reports whether it had to connect.
func composed(t *testing.T, base Shape, profile, negotiated media.Capabilities) (Row, bool) {
	t.Helper()
	shape := base
	shape.Renderer = profile
	if row, ok := Compose(shape, ProfileOnly); ok {
		return row, false
	}
	shape.Renderer = negotiated
	row, ok := Compose(shape, Negotiated)
	if !ok {
		t.Fatal("the negotiated pass composed nothing")
	}
	return row, true
}

func TestTheCompositionARendererGets(t *testing.T) {
	selfFetching := media.Capabilities{
		SelfFetch:  true,
		Containers: []string{media.MP4},
		Video:      []media.VideoSupport{{Codec: media.CodecH264}},
		Audio:      []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
	}
	for _, tt := range []struct {
		name       string
		profile    media.Capabilities
		sourceCT   string
		preference DeliveryPreference
		want       string
		connects   bool
	}{
		{"a push-only renderer is read once, decided without connecting", media.Capabilities{}, media.MP4, DeliveryAuto, "read-once", false},
		{"a self-fetching renderer that accepts the source is handed the URL", selfFetching, media.MP4, DeliveryAuto, "passthrough", true},
		{"a rejected container is remuxed", selfFetching, media.MKV, DeliveryAuto, "remux", true},
		{"the serve preference relays what could have been handed over", selfFetching, media.MP4, DeliveryServe, "remux", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			candidate := &source.Stream{
				URL:         &url.URL{Scheme: "https", Host: "cdn.example", Path: "/movie"},
				ContentType: tt.sourceCT,
				Probe:       &media.ProbeInfo{VideoCodec: media.CodecH264, VideoBitDepth: 8, AudioCodec: media.CodecAAC, AudioChannels: 2},
			}
			program, err := source.ProgramFor(candidate)
			if err != nil {
				t.Fatal(err)
			}
			profile := media.Capabilities{SelfFetch: tt.profile.SelfFetch}
			row, connected := composed(t, Shape{Program: program, Delivery: tt.preference}, profile, tt.profile)
			if row.Name != tt.want || connected != tt.connects {
				t.Errorf("composed %q (connected %v), want %q (connected %v)", row.Name, connected, tt.want, tt.connects)
			}
		})
	}
}
