// Package compose decides the shape of a cast: handed to a device that fetches for itself, remuxed on the fly, or read once and served.
package compose

// Row is one cast shape: what castor does with the bytes, and why.
type Row struct {
	Name string
	Why  string

	Kind Kind

	// when is nil on the fallback row, which is never asked.
	when func(Shape) bool
}

// Kind is what castor does with the bytes of one cast.
type Kind int

const (
	// Handoff: the device fetches the source itself.
	Handoff Kind = iota
	// Remux: castor reads the source and serves what it produced.
	Remux
	// ReadOnce: castor buffers the source locally and serves what it produced from the buffer.
	ReadOnce
)

// compositions is what a cast can be besides a remux, asked in declaration order, which is the contract.
var compositions = []Row{{
	Name: "read-once",
	Why:  "the device never fetches for itself, so this cast is served whatever it advertises",
	Kind: ReadOnce,
	when: func(s Shape) bool { return !s.Device.SelfFetch },
}, {
	Name: "handoff",
	Why:  "the device fetches for itself and already accepts the source as it is",
	Kind: Handoff,
	when: Shape.handoff,
}}

// remux answers what no composition did: the device rejects the source, or its headers do not match.
var remux = Row{
	Name: "remux",
	Why:  "the device fetches for itself but cannot be handed this source",
	Kind: Remux,
}

// For is the composition shape s calls for.
func For(s Shape) Row {
	for _, row := range compositions {
		if row.when(s) {
			return row
		}
	}
	return remux
}
