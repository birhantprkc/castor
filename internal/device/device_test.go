package device

import (
	"reflect"
	"testing"

	"github.com/stupside/castor/internal/media"
)

// TestProfileAgreesWithWhatEachFamilyReportsConnected is the invariant a cast composed
// before connecting rests on, and it used to be a sentence in a comment.
//
// The layer above starts reading a single-use source on the strength of the static
// self-fetch fact, connecting alongside. If a family's profile disagreed with what it
// reports once connected, that cast would be composed for a renderer that does not exist:
// the URL is spent by then, and nothing downstream can go back and read it again.
//
// Every shape a connected device of each family can report is listed, including DLNA's two
// (a negotiated envelope and the conservative floor), because the fact has to hold for all
// of them and not just for the happy path.
func TestProfileAgreesWithWhatEachFamilyReportsConnected(t *testing.T) {
	connected := map[Type][]media.Renderer{
		TypeChromecast: {chromecastCapabilities},
		TypeRoku:       {rokuCapabilities},
		TypeDLNA: {
			fallbackCaps(),
			// A real Sink line, so the negotiated shape is the one a device produced and
			// not one written to agree.
			parseSinkProtocolInfo("http-get:*:video/mpeg:DLNA.ORG_PN=MPEG_PS_PAL,http-get:*:video/mp4:DLNA.ORG_PN=AVC_MP4_MP_HD_720p_AAC"),
		},
	}

	for _, r := range renderers {
		caps, ok := connected[r.Type]
		if !ok {
			t.Errorf("family %q reports no connected capabilities here, so its profile is unproven", r.Type)
			continue
		}
		for _, c := range caps {
			if got := Profile(r.Type).SelfFetch; got != c.SelfFetch {
				t.Errorf("Profile(%q).SelfFetch = %v, while a connected one reports %v", r.Type, got, c.SelfFetch)
			}
		}
	}
}

// TestAProfileMeasuresNothingButSelfFetch pins the zero-elsewhere convention the
// composition's pre-connect pass depends on. A profile that started answering a container
// question would make a rule that cannot be answered before connecting look answerable, and
// the wrong answer there is a cast composed against capabilities nobody negotiated.
func TestAProfileMeasuresNothingButSelfFetch(t *testing.T) {
	for _, r := range renderers {
		p := Profile(r.Type)
		p.SelfFetch = false
		if !reflect.DeepEqual(p, media.Renderer{}) {
			t.Errorf("Profile(%q) measures more than the self-fetch fact: %+v", r.Type, p)
		}
	}

	if p := Profile(Type("nothing-castor-speaks")); !reflect.DeepEqual(p, media.Renderer{}) {
		t.Errorf("an unregistered family profiles as %+v, want nothing known about it", p)
	}
}
