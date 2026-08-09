package core

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stupside/castor/internal/media"
)

// TestPassthrough pins the delivery decision that replaced the two per-device strategies,
// and that the composition table now reads as one row's rule: given the renderer's
// advertised capabilities (SelfFetch plus the containers it takes), the source, the cast's
// height ceiling and the operator's one knob, a cast is either handed over untouched or
// produced locally.
//
// Every row here was a row of the old planner's matrix and asserts the same answer. What
// left the value is the two axes that are now structural rather than computed: the served
// container is the renderer's own declaration (see ServedFormat) and subtitles belong to
// the one composition that draws them, so neither can be carried into a cast that would
// ignore it.
func TestPassthrough(t *testing.T) {
	// caps builds a renderer that fetches for itself (or not) and accepts the given
	// containers. The audio/video support lists are irrelevant here (they feed the
	// copy-vs-encode decision, tested in resolve_test), so they stay empty.
	caps := func(selfFetch bool, containers ...string) media.Renderer {
		return media.Renderer{SelfFetch: selfFetch, Containers: containers}
	}

	tests := []struct {
		name        string
		caps        media.Renderer
		sourceCT    string
		headers     http.Header
		demuxed     bool
		leniency    bool
		height      int
		maxHeight   int
		preference  DeliveryPreference
		passthrough bool
	}{{
		// Direct play: the renderer fetches for itself and already takes the source
		// container, so the URL is handed straight to it and castor stays out of the way.
		name:        "a renderer that fetches for itself and accepts the container is handed the URL",
		caps:        caps(true, media.MP4),
		sourceCT:    media.MP4,
		passthrough: true,
	}, {
		// A source castor could only fetch with the request headers it captured is not
		// passed through even when the renderer takes the container: the renderer is handed
		// the URL and none of the headers, so it would fetch nothing (it loads the URL,
		// then idles).
		name:     "a header-gated source is served even to a renderer that accepts it",
		caps:     caps(true, media.HLS, media.MP4),
		sourceCT: media.HLS,
		headers:  http.Header{"Referer": {"https://player.example/"}, "Origin": {"https://player.example"}},
	}, {
		// One URL is one rendition, so a renderer handed it would play the video and none
		// of the audio.
		name:     "a demuxed program is served, since one URL carries only half of it",
		caps:     caps(true, media.HLS, media.MP4),
		sourceCT: media.HLS,
		demuxed:  true,
	}, {
		// A renderer fetching for itself applies its own default checks, and refuses what
		// castor had to relax one to read at all.
		name:     "a source only a lenient reader opens is served",
		caps:     caps(true, media.HLS, media.MP4),
		sourceCT: media.HLS,
		leniency: true,
	}, {
		// The same source without headers is self-sufficient: a direct URL a user casts by
		// hand still passes through, no local ffmpeg.
		name:        "a header-free source of an accepted container still passes through",
		caps:        caps(true, media.HLS, media.MP4),
		sourceCT:    media.HLS,
		passthrough: true,
	}, {
		// The operator's override, for a source nothing else convicts: it needs no headers
		// and the renderer takes the container, yet the receiver refuses it.
		name:       "configured serve overrides an otherwise pass-through cast",
		caps:       caps(true, media.HLS, media.MP4),
		sourceCT:   media.HLS,
		preference: DeliveryServe,
	}, {
		// The explicit default reads like no key at all: nothing is overridden and the
		// evidence decides.
		name:        "configured auto leaves the decision to the evidence",
		caps:        caps(true, media.HLS, media.MP4),
		sourceCT:    media.HLS,
		preference:  DeliveryAuto,
		passthrough: true,
	}, {
		// It fetches for itself but rejects the source container, so castor produces one it
		// takes.
		name:     "a renderer that rejects the source container is served a remux",
		caps:     caps(true, media.MP4),
		sourceCT: media.MKV,
	}, {
		// The AND in the rule: accepting a container is not on its own pass-through, since
		// a push-only renderer can only play what castor serves it.
		name:     "accepting the container without fetching for itself is still served",
		caps:     caps(false, media.MP4),
		sourceCT: media.MP4,
	}, {
		name:     "a renderer that never fetches for itself is always served",
		caps:     caps(false),
		sourceCT: media.MKV,
	}, {
		// The ceiling, on the one shape that used to be exempt from it. Castor downscales
		// nothing on a pass-through because it reads nothing, so handing this URL over
		// delivers 1600 lines to an operator who asked for 1080 and no leg downstream ever
		// consults the ceiling again: the two that do are the two that measure something.
		// These are the real numbers from the run this came from, a sole rendition declared
		// 3840x1600 at 18505 kb/s.
		name:      "a source declared above the ceiling is served, since a pass-through cannot be downscaled",
		caps:      caps(true, media.HLS, media.MP4),
		sourceCT:  media.HLS,
		height:    1600,
		maxHeight: 1080,
	}, {
		// Inclusive, in the same direction the encode decision reads it: a source at exactly
		// the configured height is what the operator asked for, not one line too many.
		name:        "a source declared at exactly the ceiling still passes through",
		caps:        caps(true, media.HLS, media.MP4),
		sourceCT:    media.HLS,
		height:      1080,
		maxHeight:   1080,
		passthrough: true,
	}, {
		// The carve-out, and it is required rather than a softening: a media playlist declares
		// no RESOLUTION and a direct file declares nothing at all, so convicting on an
		// undeclared height would disqualify nearly every pass-through castor makes, to bound
		// a picture that in all likelihood already fits.
		name:        "a source that declared no height at all passes through",
		caps:        caps(true, media.HLS, media.MP4),
		sourceCT:    media.HLS,
		maxHeight:   1080,
		passthrough: true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &media.Stream{ContentType: tt.sourceCT, Headers: tt.headers, NeedsLeniency: tt.leniency}
			if tt.demuxed {
				source.AudioURL = &url.URL{Scheme: "https", Host: "cdn.example", Path: "/audio.m3u8"}
			}
			shape := Shape{
				Renderer: tt.caps, Source: source, Delivery: tt.preference,
				Height: tt.height, MaxHeight: tt.maxHeight,
			}

			if got := shape.Passthrough(); got != tt.passthrough {
				t.Errorf("Passthrough() = %v, want %v (shape: %s)", got, tt.passthrough, shape)
			}
		})
	}
}

// TestAShapeNamesTheCeilingItWasComposedUnder pins the attribution half of the ceiling's
// reach into the composition, and it is not decoration.
//
// A pass-through refused for the ceiling is reported as a remux, on grounds ("cannot be
// handed this source") that are true of it without naming the fact that refused it, and the
// forced-transcode line that does name numbers comes minutes later from the encode decision,
// only if that leg's own probe agrees with what the source declared. So this line is where a
// user reads why the cheapest leg was not taken, and which of the two numbers to change.
func TestAShapeNamesTheCeilingItWasComposedUnder(t *testing.T) {
	shape := Shape{
		Renderer: media.Renderer{SelfFetch: true, Containers: []string{media.HLS}},
		Source:   &media.Stream{ContentType: media.HLS},
		Height:   1600, MaxHeight: 1080,
	}
	if shape.Passthrough() {
		t.Fatal("a source declared above the ceiling was handed to the renderer untouched")
	}
	for _, want := range []string{"source_height=1600", "max_height=1080"} {
		if !strings.Contains(shape.String(), want) {
			t.Errorf("the shape line %q does not carry %q, so nothing says which number refused the pass-through", shape, want)
		}
	}
}

// TestServedFormatIsTheRenderersOwnDeclaration covers the axis that used to be computed
// into a plan field: what a served cast produces is what the renderer asked to be served,
// and a renderer asking for something castor cannot mux is an error naming ITS declaration.
// A served leg used to build its encode from a fabricated capability record naming MPEG-TS,
// so a family declaring anything else was served something it never asked for and this
// lookup could only ever succeed.
func TestServedFormatIsTheRenderersOwnDeclaration(t *testing.T) {
	for _, container := range []string{media.MPEGTS, media.MP4, media.HLS} {
		format, err := ServedFormat(media.Renderer{ServedContainer: container})
		if err != nil {
			t.Fatalf("a renderer asking for %s: %v", container, err)
		}
		if format.ContentType != container {
			t.Errorf("served %q, want the renderer's own %q", format.ContentType, container)
		}
	}

	_, err := ServedFormat(media.Renderer{ServedContainer: "video/x-nothing-castor-muxes"})
	if err == nil {
		t.Fatal("a renderer asking for a container castor cannot produce reported success")
	}
	if !strings.Contains(err.Error(), "video/x-nothing-castor-muxes") {
		t.Errorf("error = %q, which does not name what the renderer asked for", err)
	}

	// A renderer that declared nothing is the same failure and must not resolve to a
	// container castor picked for it.
	if _, err := ServedFormat(media.Renderer{}); err == nil {
		t.Error("a renderer that declared no served container was served one anyway")
	}
}
