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

// TestOnlyAFamilyThatCanAskStatesWhatItDecodes pins the line between a capability and a
// guess about a model.
//
// A family name is not a device. "Chromecast" spans receivers a decade apart, and a first
// generation decodes no HEVC where an Ultra decodes it at half the bitrate of H.264;
// "roku" spans an Express and a Stick 4K the same way. The only thing castor is told is
// the word the operator typed, so a video envelope written per family is an assertion
// about hardware nobody identified. It is not free to make: the envelope decides which
// codec a forced re-encode AIMS at, so declaring the family's best profile points every
// such cast at a codec some of those devices cannot decode, and a black screen is a worse
// answer than a larger H.264 stream.
//
// DLNA is the family that may state one, because it does not guess: it asks the device
// (GetProtocolInfo) and only falls back to this conservative envelope when the answer is
// unusable. The rule is therefore "declare what you negotiated, or declare nothing", and
// nothing means unknown rather than none.
func TestOnlyAFamilyThatCanAskStatesWhatItDecodes(t *testing.T) {
	// The floor a negotiated envelope has to clear: H.264 High 8-bit is what a source
	// publishes when it publishes for players in general.
	baseline := media.ProbeInfo{VideoCodec: media.CodecH264, VideoProfile: "High", VideoBitDepth: 8}

	if caps := fallbackCaps(); !caps.CanCopyVideo(baseline) {
		t.Error("the negotiated family's fallback refuses 8-bit High-profile H.264, so a source published for players in general is re-encoded for a device that asked for none of that")
	}

	for _, caps := range []struct {
		family Type
		of     media.Renderer
	}{
		{TypeChromecast, chromecastCapabilities},
		{TypeRoku, rokuCapabilities},
	} {
		if len(caps.of.Video) != 0 {
			t.Errorf("family %q states a video envelope of %+v, but it has no way to ask this device what it decodes: the model is what decides, and a forced re-encode would aim at that codec for every receiver wearing the family name",
				caps.family, caps.of.Video)
		}
	}
}

// TestADeclaredEnvelopeIsTheOneEnvelopeTheCodecHas pins that the envelope a negotiated
// codec is paired with comes from codecEnvelopes rather than being restated. The profile
// and bit-depth lists are what separate a copy that plays from a green smear (High 10 is
// the one that bites on H.264), and a second copy of them is how two callers end up
// disagreeing about which.
func TestADeclaredEnvelopeIsTheOneEnvelopeTheCodecHas(t *testing.T) {
	caps := fallbackCaps()
	if len(caps.Video) == 0 {
		t.Fatal("the fallback declares no video at all, so this pins nothing")
	}
	for _, got := range caps.Video {
		if want := videoSupportFor(got.Codec); !reflect.DeepEqual(got, want) {
			t.Errorf("a declared %s envelope is %+v, want the one codecEnvelopes states: %+v", got.Codec, got, want)
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
