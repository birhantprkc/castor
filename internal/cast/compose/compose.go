// Package compose decides the shape of a cast: handed to a renderer that fetches for itself, remuxed on the fly, or read once and served.
package compose

// Row is one cast shape: what castor does with the bytes, and why.
type Row struct {
	// Shape identifier and reasoning for auditability.
	Name string
	Why  string

	Kind Kind

	// needs is what this row's rule reads, and therefore which pass may evaluate it.
	needs Needs

	// Rule predicate; nil on total row (never asked).
	when func(Shape) bool
}

// Kind is what castor does with the bytes of one cast.
type Kind int

const (
	// Handoff: the renderer fetches the source itself.
	Handoff Kind = iota
	// Remux: castor reads the source and serves what it produced.
	Remux
	// ReadOnce: castor buffers the source locally and serves what it produced from the buffer.
	ReadOnce
)

// Needs is the facts a rule reads, ordered; it controls when the renderer is acquired.
type Needs int

const (
	// Answerable from family's static profile.
	ProfileOnly Needs = iota
	// Requires connected renderer; costs connect.
	Negotiated
)

// compositions is what a cast can be besides a remux, asked in declaration order, which is the contract.
var compositions = []Row{{
	// Non-fetching renderer; castor serves; early read before device connect.
	Name:  "read-once",
	Why:   "the renderer never fetches for itself, so this cast is served whatever it advertises",
	Kind:  ReadOnce,
	needs: ProfileOnly,
	when:  func(s Shape) bool { return !s.Renderer.SelfFetch },
}, {
	// Renderer accepts source as-is; no encoding or scaling needed.
	Name:  "passthrough",
	Why:   "the renderer fetches for itself and already accepts the source as it is",
	Kind:  Handoff,
	needs: Negotiated,
	when:  Shape.passthrough,
}}

// remux answers what no composition did: the renderer rejects the source, or its headers do not match.
var remux = Row{
	Name:  "remux",
	Why:   "the renderer fetches for itself but cannot be handed this source",
	Kind:  Remux,
	needs: Negotiated,
}

// Compose answers row for shape; two-pass (profile, then negotiated).
func Compose(s Shape, admits Needs) (Row, bool) {
	for _, row := range compositions {
		if row.needs <= admits && row.when(s) {
			return row, true
		}
	}
	if remux.needs > admits {
		return Row{}, false
	}
	return remux, true
}
