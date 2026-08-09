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

// TestEveryFamilyDeclaresTheVideoItDecodes closes the gap a nil envelope left. Two
// families carried none, described in their own comments as deliberate because the leg
// they land on copies whatever the source is, which is a fact about a leg: the field is
// read by the layer that has to choose a codec when a copy is refused, and it answered
// there with "this renderer advertised nothing we can encode to", spending a whole title
// on the floor codec for a device whose vendor publishes HEVC.
//
// It is stated over the registry rather than per family, so a fourth family cannot ship
// with the field left at nil and the same reasoning rediscovered a third time.
func TestEveryFamilyDeclaresTheVideoItDecodes(t *testing.T) {
	// The floor every family clears: H.264 is what a source publishes when it publishes
	// for players in general, and a family that cannot decode it cannot be cast to at all.
	baseline := media.ProbeInfo{VideoCodec: media.CodecH264, VideoProfile: "High", VideoBitDepth: 8}

	declared := map[Type]media.Renderer{
		TypeChromecast: chromecastCapabilities,
		TypeRoku:       rokuCapabilities,
		TypeDLNA:       fallbackCaps(),
	}
	for _, r := range renderers {
		caps, ok := declared[r.Type]
		if !ok {
			t.Errorf("family %q states no capabilities here, so its video envelope is unproven", r.Type)
			continue
		}
		if len(caps.Video) == 0 {
			t.Errorf("family %q declares no video envelope at all, so every re-encode for it aims at the floor codec and every copy gate reads a renderer that said nothing", r.Type)
			continue
		}
		if !caps.CanCopyVideo(baseline) {
			t.Errorf("family %q does not accept 8-bit High-profile H.264, which is what a source publishes for players in general", r.Type)
		}
	}
}

// TestADeclaredEnvelopeIsTheOneEnvelopeTheCodecHas pins that the families which declare
// rather than negotiate declare the SAME envelope DLNA pairs with a negotiated codec. The
// profile and bit-depth lists are what separate a copy that plays from a green smear (High
// 10 is the one that bites on H.264), and three copies of them is how two families end up
// disagreeing about which.
func TestADeclaredEnvelopeIsTheOneEnvelopeTheCodecHas(t *testing.T) {
	for _, caps := range []media.Renderer{chromecastCapabilities, rokuCapabilities} {
		for _, got := range caps.Video {
			if want := videoSupportFor(got.Codec); !reflect.DeepEqual(got, want) {
				t.Errorf("a declared %s envelope is %+v, want the one codecEnvelopes states for every family: %+v", got.Codec, got, want)
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
