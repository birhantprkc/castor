package compose

import (
	"net/url"
	"testing"

	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source"
)

func TestTheCompositionADeviceGets(t *testing.T) {
	selfFetching := media.Capabilities{
		SelfFetch:  true,
		Containers: []string{media.MP4},
		Video:      []media.VideoSupport{{Codec: media.CodecH264}},
		Audio:      []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
	}
	for _, tt := range []struct {
		name       string
		device     media.Capabilities
		sourceCT   string
		preference DeliveryPreference
		want       string
	}{
		{"a push-only device is read once", media.Capabilities{}, media.MP4, DeliveryAuto, "read-once"},
		{"a self-fetching device that accepts the source is handed the URL", selfFetching, media.MP4, DeliveryAuto, "handoff"},
		{"a rejected container is remuxed", selfFetching, media.MKV, DeliveryAuto, "remux"},
		{"the serve preference relays what could have been handed over", selfFetching, media.MP4, DeliveryServe, "remux"},
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
			if row := Compose(Shape{Device: tt.device, Program: program, Delivery: tt.preference}); row.Name != tt.want {
				t.Errorf("composed %q, want %q", row.Name, tt.want)
			}
		})
	}
}
