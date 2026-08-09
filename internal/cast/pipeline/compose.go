package pipeline

import (
	"context"
	"fmt"

	"github.com/stupside/castor/internal/cast/core"
)

// composition is one shape a cast can take: the rule that chooses it, when the renderer is
// acquired for it, how much its copy may refuse, and the wiring that runs it.
//
// The last two columns are the point. They were inline literals in two long functions, one
// saying CopyWhatever and the other CopyWhatFits with a paragraph each about why, and the
// connect timing was a fork at the top of the executor. As columns they are what one row
// differs from another BY, so a fourth composition is a row rather than an edit to control
// flow, and no wiring function has a decision left in it to get wrong.
type composition struct {
	// name and why are what a run has to be readable as: which shape this cast took, and on
	// what grounds.
	name string
	why  string

	// needs is what this row's rule reads, and therefore which pass may evaluate it.
	needs needs

	// when is the rule. It is pure and reads nothing but the shape, so every row is a
	// table-driven test over values.
	when func(core.Shape) bool

	// connect is when the renderer is acquired for a cast of this shape.
	connect timing

	// policy is how much this composition's copy is allowed to refuse. It is zero on a
	// composition that produces no encode, where there is nothing for a copy rule to answer
	// about.
	policy core.VideoPolicy

	// run is the wiring, and it holds no decision: everything a stream's pathology could
	// change arrives on the attempt or in this row.
	run leg
}

// leg is one composition's wiring: it starts the stages the row names, in the order they
// have to start, and reports how far the cast got.
type leg func(ctx context.Context, c *cast) landing

// needs is which facts a row's rule reads, and it is ORDERED: a pass admits every row whose
// needs it can satisfy, so the pre-connect pass admits profileOnly rows and the post-connect
// pass admits all of them.
type needs int

const (
	// profileOnly rows are answerable from the family's static profile, before anything has
	// been discovered.
	profileOnly needs = iota
	// negotiated rows read what a connected renderer answered, so evaluating one costs a
	// connect.
	negotiated
)

// timing is when the renderer is acquired relative to the read.
type timing int

const (
	// connectFirst acquires the renderer before the read starts. Every negotiated row is
	// necessarily this, because choosing it is what already acquired one.
	connectFirst timing = iota
	// connectConcurrent starts reading immediately and acquires the renderer alongside,
	// because the shape of the cast is already fixed and discovery plus connect can take
	// seconds a short-lived signed URL cannot spare.
	connectConcurrent
)

func (t timing) String() string {
	if t == connectConcurrent {
		return "concurrently with the read"
	}
	return "before the read"
}

// compositions is the whole of what a cast can be, in the order rows are asked. It replaced
// two nested booleans, and it reproduces them exactly.
var compositions = []composition{{
	// A renderer that never fetches for itself can only play what castor serves it, so this
	// cast is a served buffer whatever the renderer turns out to advertise. That is knowable
	// from the family alone, which is what lets the read start first: SSDP discovery plus
	// connect can take seconds, and they are seconds a single-use signed URL does not have.
	//
	// It is also the only composition castor produces the picture for, which is why a burn-in
	// can only ever happen here: the other two hand over a container, not decoded frames.
	name:    "read-once",
	why:     "the renderer never fetches for itself, so this cast is served whatever it advertises",
	needs:   profileOnly,
	when:    func(s core.Shape) bool { return !s.Renderer.SelfFetch },
	connect: connectConcurrent,
	// The buffer is read by an encode produced for one renderer that has already answered,
	// so it holds that renderer to what it advertised: an envelope it did not name is
	// re-encoded rather than gambled on. This is the only composition that can also be
	// forced to produce the picture by a burn-in.
	policy: core.CopyWhatFits,
	run:    readOnce,
}, {
	// Nothing to do: the renderer fetches for itself, the source needs nothing but its URL,
	// the container is already one it takes, and nothing says the picture is taller than the
	// cast's ceiling. Castor touches none of the media.
	//
	// That last clause is why the ceiling is a term of the composition and not only of an
	// encode: castor cannot downscale a URL it never reads, so the ONLY way to keep a source
	// declared above max_height off the renderer is to refuse this row and let the total row
	// below serve a scaled remux instead (see core.Shape.Passthrough for the cost, and for
	// why an undeclared height still passes through).
	name:    "passthrough",
	why:     "the renderer fetches for itself and already accepts the source as it is",
	needs:   negotiated,
	when:    core.Shape.Passthrough,
	connect: connectFirst,
	// No policy: this composition produces no encode, so no copy rule is asked anything.
	run: passthrough,
}, {
	// The total row, and the reason a missing composition is nearly unreachable rather than
	// merely reported: a renderer that fetches for itself but cannot be handed this source
	// (it rejects the container, or the source only answers to the request headers castor
	// holds and a renderer is handed none of them) is served a remux of it.
	name:    "remux",
	why:     "the renderer fetches for itself but cannot be handed this source",
	needs:   negotiated,
	when:    func(core.Shape) bool { return true },
	connect: connectFirst,
	// A remux changes the wrapper and not the picture, so the bitstream it copies is the one
	// the source published for players in general, and holding it to what this renderer
	// happened to advertise buys a whole title of re-encode against a device that probably
	// decodes it anyway. What the policy does NOT lift is the cast's height ceiling (a user
	// instruction, not a judgement about the renderer) or carriage (a fact about the muxer,
	// whose only effect is to route a doomed copy to the re-encode ladder).
	policy: core.CopyWhatever,
	run:    remux,
}}

// compose chooses the composition this cast runs, in two passes, and that is what makes the
// table honest rather than circular. A rule that reads what a renderer negotiated cannot be
// asked before one has been acquired, and acquiring one before the read starts is exactly
// the delay a short-lived signed URL cannot afford. So the first pass asks only the rows the
// static family profile answers; only if none of them does is the renderer acquired and
// every row asked.
//
// Once a first-pass row has matched, no negotiated row is reachable at all: the renderer is
// never acquired, so there is nothing for one to read. That is the whole of the contract the
// prose used to carry (the static fact "MUST agree" with the connected one), and
// device.Profile is what makes it sound, answering that one fact and leaving every other
// field zero.
//
// base is the cast as its caller already knows it: the link, what the source declared about
// it, the ceiling the operator set, and the operator's say over delivery. The only two fields
// this fills in are the two only it can, the renderer and whether one has answered yet, which
// is exactly the difference between the two passes.
func compose(ctx context.Context, rows []composition, t Target, renderer *held, base core.Shape) (composition, core.Shape, error) {
	shape := base
	shape.Renderer, shape.Negotiated = t.Profile(), false
	if row, ok := match(rows, shape, profileOnly); ok {
		return row, shape, nil
	}

	dev, err := renderer.get(ctx)
	if err != nil {
		return composition{}, shape, err
	}
	shape.Renderer, shape.Negotiated = dev.Capabilities(), true
	if row, ok := match(rows, shape, negotiated); ok {
		return row, shape, nil
	}
	return composition{}, shape, fmt.Errorf("no composition for a cast of this shape: %s", shape)
}

// match answers with the first row this pass may ask that the shape satisfies. Declaration
// order is the contract, as it is in every other table here: a row placed above another
// shadows it deliberately.
func match(rows []composition, s core.Shape, admits needs) (composition, bool) {
	for _, row := range rows {
		if row.needs > admits {
			continue
		}
		if row.when(s) {
			return row, true
		}
	}
	return composition{}, false
}
