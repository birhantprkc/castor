package core

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/stupside/castor/internal/media"
)

// Shape is the cast a composition is chosen from: the renderer as far as it is known, the
// source it is being asked to carry along with what that source declared about it, the
// ceiling the operator set, and the operator's one say over how. It is pure data with no
// I/O behind it, which is what lets the whole composition question be answered by rules
// over a value rather than by a planner nobody can call twice.
//
// It carries no probe, deliberately, and Height is the one number rather than an exception
// to that: what a cast is made of has to be settled before a byte is read, so a composition
// reads facts already established about the link and never opens it to find out how to open
// it. The two served compositions each measure their own subject once their leg runs (the
// upstream for a remux, the local buffer for a read-once), and that measurement is theirs.
type Shape struct {
	// Renderer is what is known about the renderer: the static family profile before one
	// has been acquired, its negotiated capabilities after. Zero means unmeasured
	// everywhere in it (see media.Renderer's zero conventions), so a rule reading a field
	// no profile fills is a rule that cannot be answered before connecting, which is
	// exactly what the composition table's two passes are split on.
	Renderer media.Renderer

	// Source is the link this cast reads.
	Source *media.Stream

	// Height is the tallest thing ESTABLISHED about the picture behind this link, whichever
	// party established it: the rung the source declared for what the cast will read (an HLS
	// RESOLUTION) where there is one, else the height a probe measured while candidates were
	// ranked. The ceiling has to bind whichever one exists, and only the declaration used to
	// arrive (see media.Stream.Height). 0 means neither, and is the absence of evidence it is.
	Height int

	// MaxHeight is the cast's height ceiling, the number the operator typed. It is here
	// because the ceiling binds the choice of composition and not only the encode: it is a
	// maximum on what reaches the RENDERER, so the one shape castor cannot downscale is
	// the one it has to refuse rather than the one it exempts (see Passthrough).
	MaxHeight media.HeightCap

	// Delivery is the operator's answer on the delivery axis, and the only operator-facing
	// axis a cast has.
	Delivery media.DeliveryPreference

	// Negotiated reports whether Renderer is what a connected renderer answered or the
	// family profile that was knowable without one. It is the difference between "this
	// renderer declared no container castor can produce" and "nobody has asked it yet".
	Negotiated bool
}

// Passthrough reports whether the renderer can simply be handed the source URL and left to
// fetch the bytes itself, which is true only when all four of these hold:
//
//   - the renderer fetches for itself (a push-only one only plays what castor serves it);
//   - the source is reachable from the URL alone: a pass-through carries none of the
//     request headers castor captured, so a header-gated source must be read by castor and
//     served (see media.Stream.SelfFetchable);
//   - the renderer already accepts the source container, so there is nothing to rewrap;
//   - the source is not KNOWN to be taller than the cast's ceiling, whether the source
//     declared that height or a probe measured it. max_height is a maximum on what reaches the
//     renderer, not on what castor's encoder happens to produce, and castor cannot downscale a
//     URL it never reads: handing over a 3840x1600 rung delivers 4K to an operator who asked
//     for 1080, and no line downstream would mention it, because the legs that consult the
//     ceiling are the legs that measure something. Refusing the shape routes it to the remux,
//     which reads the source and scales it (see DecideVideo), and that is the expensive leg on
//     purpose: the alternative is casting more than was asked for.
//
// An UNESTABLISHED height (0) passes, on the leniency media.HeightCap.Admits states once for
// every party that honours the ceiling. The carve-out is required rather than a softening (a
// URL cast by hand is never probed and declares nothing) and it is not a licence for a
// measured source, which is what it silently became while the measurement went uncarried.
//
// Configured media.DeliveryServe skips the question. It is the operator's answer for a source
// none of the evidence above can convict, a source that lies about itself (a playlist whose
// segments are served under a disguised extension, say) and so is fetchable as far as castor
// can tell while the renderer refuses it. Every other value, including the unset one, leaves
// the decision to the evidence.
func (s Shape) Passthrough() bool {
	if s.Delivery == media.DeliveryServe {
		return false
	}
	return s.Renderer.SelfFetch && s.Source.SelfFetchable() &&
		s.Renderer.AcceptsContainer(s.Source.ContentType) && s.MaxHeight.Admits(s.Height)
}

// String is the one line a cast's shape has to be readable as, because it is what a
// composition nobody wrote a row for has to be reported in, and what says why a renderer
// that would have taken the source container is being served instead (a header-gated
// source names its keys here rather than in a second log line).
//
// The two heights are here for that second reason and are load-bearing. A pass-through
// refused for a picture established above the ceiling is reported as a remux like
// every other served cast, and the composition's own grounds ("cannot be handed this
// source") are true of it without saying which fact refused it. These are the numbers a
// user can act on, and the only place they appear before the read: the forced-transcode
// line comes from the encode decision minutes later, and only if the leg's own probe agrees.
func (s Shape) String() string {
	source := media.Stream{}
	if s.Source != nil {
		source = *s.Source
	}
	return fmt.Sprintf("self_fetch=%v negotiated=%v served_container=%s source_content_type=%s source_self_fetchable=%v source_header_keys=%v source_height=%d max_height=%d delivery=%s",
		s.Renderer.SelfFetch, s.Negotiated,
		cmp.Or(s.Renderer.ServedContainer, "none"),
		cmp.Or(source.ContentType, "unknown"),
		source.SelfFetchable(),
		slices.Sorted(maps.Keys(source.Headers)),
		s.Height, s.MaxHeight,
		cmp.Or(s.Delivery, media.DeliveryAuto))
}

// ServedFormat is the container a served cast produces, as the registry describes it: the
// renderer's own declaration, read from it rather than assumed. Core bakes in no container
// of its own, so no device family's format choice lives here, and a renderer asking for one
// castor cannot mux is an error naming ITS declaration rather than a value castor invented
// (a served leg used to build its plan from a fabricated capability record naming MPEG-TS,
// so a family declaring anything else was served something it never asked for and this
// lookup could only ever succeed).
func ServedFormat(caps media.Renderer) (media.FormatInfo, error) {
	format, ok := media.FormatForContentType(caps.ServedContainer)
	if !ok {
		return media.FormatInfo{}, fmt.Errorf("the renderer asks to be served %q, which castor cannot produce", caps.ServedContainer)
	}
	return format, nil
}
