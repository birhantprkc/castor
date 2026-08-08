package core

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/stupside/castor/internal/media"
)

// Shape is the cast a composition is chosen from: the renderer as far as it is known, the
// source it is being asked to carry, and the operator's one say over how. It is pure data
// with no I/O behind it, which is what lets the whole composition question be answered by
// rules over a value rather than by a planner nobody can call twice.
//
// It carries no probe, deliberately. What a cast is made of has to be settled before a
// byte is read: the two served compositions each measure their own subject once their leg
// runs (the upstream for a remux, the local buffer for a read-once), and a composition
// that needed a measurement first would have to open the source to find out how to open
// the source.
type Shape struct {
	// Renderer is what is known about the renderer: the static family profile before one
	// has been acquired, its negotiated capabilities after. Zero means unmeasured
	// everywhere in it (see media.Renderer's zero conventions), so a rule reading a field
	// no profile fills is a rule that cannot be answered before connecting, which is
	// exactly what the composition table's two passes are split on.
	Renderer media.Renderer

	// Source is the link this cast reads.
	Source *media.Stream

	// Delivery is the operator's answer on the delivery axis, and the only operator-facing
	// axis a cast has.
	Delivery DeliveryPreference

	// Negotiated reports whether Renderer is what a connected renderer answered or the
	// family profile that was knowable without one. It is the difference between "this
	// renderer declared no container castor can produce" and "nobody has asked it yet".
	Negotiated bool
}

// Passthrough reports whether the renderer can simply be handed the source URL and left to
// fetch the bytes itself, which is true only when all three of these hold:
//
//   - the renderer fetches for itself (a push-only one only plays what castor serves it);
//   - the source is reachable from the URL alone: a pass-through carries none of the
//     request headers castor captured, so a header-gated source must be read by castor and
//     served (see media.Stream.SelfFetchable);
//   - the renderer already accepts the source container, so there is nothing to rewrap.
//
// Configured DeliveryServe skips the question. It is the operator's answer for a source
// none of the evidence above can convict, a source that lies about itself (a playlist whose
// segments are served under a disguised extension, say) and so is fetchable as far as castor
// can tell while the renderer refuses it. Every other value, including the unset one, leaves
// the decision to the evidence.
func (s Shape) Passthrough() bool {
	if s.Delivery == DeliveryServe {
		return false
	}
	return s.Renderer.SelfFetch && s.Source.SelfFetchable() && s.Renderer.AcceptsContainer(s.Source.ContentType)
}

// String is the one line a cast's shape has to be readable as, because it is what a
// composition nobody wrote a row for has to be reported in, and what says why a renderer
// that would have taken the source container is being served instead (a header-gated
// source names its keys here rather than in a second log line).
func (s Shape) String() string {
	source := media.Stream{}
	if s.Source != nil {
		source = *s.Source
	}
	return fmt.Sprintf("self_fetch=%v negotiated=%v served_container=%s source_content_type=%s source_self_fetchable=%v source_header_keys=%v delivery=%s",
		s.Renderer.SelfFetch, s.Negotiated,
		cmp.Or(s.Renderer.ServedContainer, "none"),
		cmp.Or(source.ContentType, "unknown"),
		source.SelfFetchable(),
		slices.Sorted(maps.Keys(source.Headers)),
		cmp.Or(s.Delivery, DeliveryAuto))
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
