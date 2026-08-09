package attempt

import (
	"context"
	"log/slog"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/media"
)

// Change is everything a strategy may read to decide what to try next: the material the
// whole cast was given, the attempt that just failed, what it left behind, and the one
// port a recovery is allowed to reach through.
//
// It is a value rather than four parameters because a strategy is a row of a table: the
// signature is the same for all of them and only one of them needs the port, so the
// alternative was either a port on every signature spelled out five times or two kinds of
// strategy that the playbook could not hold in one slice.
type Change struct {
	Intent  Intent
	Attempt Attempt
	Outcome Outcome

	// Program re-establishes what a link publishes, for the one recovery that changes
	// which link is read. Nothing else here performs I/O.
	Program Program
}

// Strategy is one named way to change an attempt after a fault.
//
// Applicability is a fact about the strategy and not a condition in the loop, which is
// what makes "this recovery cannot be offered here" a value the playbook falls through
// rather than a branch somebody has to remember: a source that published one rendition
// answers false to a degrade, and the loop simply asks the next strategy and eventually
// refuses with the numbers.
type Strategy struct {
	// Name identifies the strategy in the log line that announces a revision and in the
	// refusal that lists what was already spent.
	Name string
	// Why is what this change is meant to achieve, carried so a revision says why it is
	// worth a viewer's time rather than only that it happened.
	Why string
	// Apply returns the changed attempt, or false when this recovery cannot be offered.
	//
	// Every strategy must move the attempt strictly DOWN a finite order (an index up a
	// bounded list, a bitrate down a published ladder, a copy cleared that is never
	// reinstated, a pace lowered that is never raised, a delivery axis that flips one
	// way). That is what makes the loop terminate without a maximum-attempts knob, and the
	// ledger is the backstop under it: an Apply that hands back something already run
	// stalls the cast instead of spinning it.
	Apply func(context.Context, Change) (Attempt, bool)
}

// SwitchCandidate reads the next link the ranker admitted, and it is the load-bearing
// recovery for a source castor cannot get the bytes out of.
//
// It needs no second extraction and no per-site guessing: the ordering was already
// established by measuring every candidate, and everything below the head of it is a link
// castor has proof it could open. On the run this exists for, the chosen candidate
// delivered 0.159x while the ranker reported four alternatives, two of which probed
// cleanly, and the candidate's own ladder offered nothing to fall back to.
//
// It moves strictly up the ordering, which bounds it: a cast can spend at most as many
// attempts as the ranker admitted links.
var SwitchCandidate = Strategy{
	Name: "switch-candidate",
	Why:  "read the next link the ranker admitted, which was measured and opened like this one",
	Apply: func(ctx context.Context, c Change) (Attempt, bool) {
		a := c.Attempt
		next := a.Candidate + 1
		if next >= len(c.Intent.Candidates) {
			return a, false
		}
		a.Candidate = next

		// The link is resolved exactly as the head of the ordering was, and for the same
		// reasons: the height ceiling is applied by narrowing a master to a rung, a program
		// that publishes its audio separately is only playable when both renditions are read,
		// and how the segments are framed is what the read policy keys on. Handing the reader
		// an unresolved master instead lets the demuxer choose, which on these sources is how
		// a cast escaping a 4K rung that cannot deliver reaches for another one.
		a.Source, a.Origin, a.Rendition = resolved(ctx, c.Program, c.Intent.Candidates[next])
		a.Read = read.For(read.ShapeOf(a.Origin), c.Intent.Deadline)

		// A different link is a different bitstream, so what a copy of the last one broke on is
		// no evidence against this one. Clearing it costs at most one repeat of a fault the
		// ledger cannot confuse with the first (the candidate is part of an attempt's identity),
		// and keeping it would decode a whole title on the strength of another link's packets.
		a.Decode = carriage.Axes{}
		return a, true
	},
}

// resolved establishes what a link castor is moving onto publishes.
//
// A link whose documents cannot be read is attempted whole rather than abandoned, which is
// the posture the source layer already takes toward a playlist it could not fetch: the
// reader that follows carries headers, reconnects and minutes that one GET does not. What
// is then read is a source castor knows nothing about, which gets the careful row of the
// read table rather than the permissive one.
func resolved(ctx context.Context, p Program, link *media.Stream) (*media.Stream, media.Origin, media.Rendition) {
	stream, origin, rung, err := p.Refetch(ctx, link)
	if err != nil {
		slog.WarnContext(ctx, "the next link's own documents could not be read; it will be attempted as published",
			"url", link.URL.String(), "error", err)
		return link, media.Origin{}, media.Rendition{}
	}
	return stream, origin, rung
}

// DegradeRendition reads a lighter rung of the same program: the heaviest one the link has
// proved it can carry.
//
// The ceiling is measured rather than guessed, and it is the product of two facts castor
// already holds: the rate the source DECLARED for the rung being read, and the speed the
// reader actually achieved on it. A 6941 kb/s rung delivering 0.159x is a link carrying
// about 1.1 Mbit/s, and the rung to move to is the heaviest one under that. Stepping one
// place down the publication order instead would be a guess, and on a ladder ordered by
// anything but bitrate it is a guess that can land heavier than the rung it was escaping.
//
// It reports inapplicable rather than inventing a rung, and each way it does so is the
// absence of evidence rather than an answer to it: a source that published one rendition
// gave castor nothing to choose from (media.Origin.Sole), a rung whose rate the source
// never declared offers no ceiling to measure against (which is also why
// media.Origin.Lighter refuses such a rung as a destination), and a read that never stated
// a speed measured nothing.
var DegradeRendition = Strategy{
	Name: "degrade-rendition",
	Why:  "read the heaviest rung of the same program the measured link can carry",
	Apply: func(_ context.Context, c Change) (Attempt, bool) {
		a := c.Attempt
		// Stated rather than left to the arithmetic below, which reaches the same answer for
		// each of them (an unknown rate or an unmeasured speed yields a zero ceiling, and no
		// declared rung is under zero). The three ways this recovery has nothing to work with
		// are its whole contract, and a reader should not have to derive them.
		speed := c.Outcome.Evidence.Health.Speed
		if a.Origin.Sole() || a.Rendition.Bitrate <= 0 || speed <= 0 {
			return a, false
		}

		// The ceiling is capped by the rung being read, so the move is strictly down the ladder
		// whatever the measurement says. A read that fell short of playback while running
		// ahead of realtime still measures a ceiling above its own rung, and a strategy that
		// could hand back the rung that just failed is a loop with nothing but the ledger
		// between it and spinning.
		carried := media.Bitrate(float64(a.Rendition.Bitrate) * float64(speed))
		lighter := a.Origin.Lighter(min(carried, a.Rendition.Bitrate))
		if len(lighter) == 0 {
			return a, false
		}

		// The same link at a different rung, so everything that link was measured with (the
		// headers it only answers to, the leniency its segments needed, its companion audio
		// rendition, the runtime of the one program all its rungs carry) travels with it.
		// The height does not: it described the rung that just failed, and carrying a taller
		// picture's measurement is what would convict the new rung under the cast's ceiling.
		rung := lighter[0]
		source := *a.Source
		source.URL = rung.URL
		source.Height = rung.Height
		a.Source, a.Rendition = &source, rung
		return a, true
	},
}

// DecodeAxis stops copying the half of the program whose packets the reader died on, and
// decodes it into the buffer instead.
//
// It is the recovery for the one failure a cast cannot be resumed from, the bitstream a
// stream copy cannot resynchronise (read's segment-fragile row states the mechanism and
// what it costs). Decoded, the same packets cost a re-encode and arrive with nothing left
// to resynchronise.
//
// It blames every axis the reader was COPYING and not the one the prose named. Which half
// broke is not knowable from an exit status, the line that names it is prose and prose is
// not a contract, and the cost of decoding both is bounded and known: the pull's floor is
// the software H.264 baseline under a VBV cap plus stereo AAC (see media.FloorVideoCodec),
// which is the same floor the encode downstream bottoms out at.
//
// It descends strictly and it is why Decode is part of an attempt's identity: the set of
// copied axes only ever shrinks within one link, so this can be offered at most twice per
// candidate and answers false once there is nothing left to stop copying.
var DecodeAxis = Strategy{
	Name: "decode-axis",
	Why:  "stop copying the packets the reader died on and decode them, which is what a truncated bitstream needs",
	Apply: func(_ context.Context, c Change) (Attempt, bool) {
		a := c.Attempt
		next := a.Decode.Or(c.Outcome.Evidence.Copied)
		if next == a.Decode {
			return a, false
		}
		a.Decode = next
		return a, true
	},
}

// RelaxRead asks for the same link at the pace a source that has already stopped
// delivering gets: playback speed, no wire-speed burst (see read.Cautious).
//
// It is the only thing about HOW a link is read that a stall gives any reason to change,
// and the reason is castor's own: every VOD read opens by demanding ninety seconds of
// stream as fast as the wire will carry it, and a burst of requests behind one signature
// against exactly these hosts is the documented way to earn a 429 and have the address
// tarpitted. A link that went silent for two reconnect ceilings is that shape.
//
// One step, and no more: the pace it drops to has nothing left to give up, so this answers
// false the second time it is asked.
var RelaxRead = Strategy{
	Name: "relax-read",
	Why:  "ask for the same link at playback pace with no wire-speed burst, in case the burst is what it stopped answering",
	Apply: func(_ context.Context, c Change) (Attempt, bool) {
		a := c.Attempt
		policy, ok := read.Cautious(a.Read)
		if !ok {
			return a, false
		}
		a.Read = policy
		return a, true
	},
}

// ServeInstead stops handing the renderer a URL and serves it a local stream.
//
// It is the recovery for a renderer that refused what it was pointed at, and it needs no
// new evidence and no new mechanism: it sets the same value the operator's one knob writes
// (media.DeliveryServe), which the composition table already reads. The failure it answers
// is a source castor has no way to convict, one that lies about itself (a playlist whose
// segments are served under a disguised extension) and so looks fetchable while the
// renderer refuses it.
//
// It flips one way and is therefore terminal after one use: a served cast is never handed
// back the source URL, since the refusal it would be answering came from doing exactly
// that.
var ServeInstead = Strategy{
	Name: "serve-instead",
	Why:  "read the source and serve it locally, since the renderer would not fetch it itself",
	Apply: func(_ context.Context, c Change) (Attempt, bool) {
		a := c.Attempt
		if a.Delivery == media.DeliveryServe {
			return a, false
		}
		a.Delivery = media.DeliveryServe
		return a, true
	},
}
