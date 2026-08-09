package attempt

import (
	"cmp"
	"fmt"
	"time"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/media"
)

// Intent is what a whole cast has to work with: the links the ranker admitted in the
// order it ranked them, what the source published about the program behind the head of
// that ordering, how a source of that shape is fetched, and the operator's say over the
// delivery axis.
//
// Nothing in it changes while a cast runs. An Attempt is a projection of it, and what a
// recovery walks through is this material, which is why how many attempts a cast can
// make is a property of what it was given rather than of a limit inside the loop.
type Intent struct {
	// Candidates is the ordering the ranker produced, head first: every link it admitted
	// for this title, each already measured (see resolve.RankStreams). Its length is how
	// many links a cast may still try, and advancing through it is the load-bearing
	// recovery: the run this layer was written for reached for the tallest rendition of a
	// candidate that delivered 0.159x while two other admitted playlists probed cleanly.
	//
	// Only the head has been resolved. What each of the others publishes is established
	// when a cast moves onto it and not before (see SwitchCandidate and Program): a
	// playlist GET per candidate, up front, is a burst of requests against exactly the
	// hosts the ranker caps its probes per host to protect, spent on links most casts
	// never read.
	Candidates []*media.Stream

	// Origin is what the source published about the program behind the head candidate: the
	// rendition ladder, how its segments are framed, whether it ends, how long it runs. A
	// zero Origin is the honest answer for a source whose documents castor never read.
	Origin media.Origin

	// Rendition is which rung of that ladder the head candidate is, as the source declared
	// it. Zero says the source layer did not state which rung it chose, which is what a
	// recovery has to read as the absence of evidence rather than as a free rendition (see
	// Attempt.Rendition).
	Rendition media.Rendition

	// Read is how that source is fetched, chosen from its shape before anything started
	// (see read.For).
	Read read.Policy

	// Deadline is the configured mid-read deadline. It is carried beside the policy it has
	// already gone into because a recovery that changes WHAT is read has to re-derive HOW:
	// the read table takes the deadline as a term, and a row is free to apply or withhold
	// it.
	Deadline time.Duration

	// Delivery is the operator's answer on the delivery axis, and the only operator-facing
	// axis a cast has. It seeds the first attempt, which a recovery may then change.
	Delivery media.DeliveryPreference
}

// first is the attempt an intent starts from: the head of the ordering, read on the terms
// its own program was measured for, delivered the way the operator asked.
func (in Intent) first() Attempt {
	return Attempt{
		Try:       1,
		Source:    in.Candidates[0],
		Origin:    in.Origin,
		Rendition: in.Rendition,
		Read:      in.Read,
		Delivery:  in.Delivery,
	}
}

// Attempt is one fully decided try at a cast: which link is read, which rung of the
// program that link is, how it is fetched, and how the renderer is asked to receive it.
//
// It is pure data with no I/O behind it, which is what lets a cast's whole shape be
// printed on one line and driven by a scripted runner. It decides nothing either: every
// field arrives from the intent or from the strategy that changed it, so the question
// "what was castor trying when this failed" has one answer to read rather than a call
// stack to reconstruct.
type Attempt struct {
	// Try counts this attempt within the cast, from 1. It is on the attempt rather than
	// kept by the loop because it is what places a log line: two attempts of the same cast
	// otherwise print the same fields and read as one confused run.
	Try int

	// Candidate is which of the intent's links this attempt reads, as an index into
	// Intent.Candidates, and Source is that link.
	Candidate int
	Source    *media.Stream

	// Origin is what the source published about the program behind Source. It travels with
	// the candidate, because what one link published about its program says nothing about
	// another's.
	Origin media.Origin

	// Rendition is the rung of that ladder Source is, as the source declared it. It is
	// zero where the source layer did not say which rung it chose, which is the ordinary
	// case: resolution narrows a master to one URL and the variant it picked does not
	// travel with it.
	//
	// A recovery reads a zero rung as the absence of evidence and not as a free rendition,
	// for the same reason media.Origin.Lighter refuses an undeclared bitrate as a
	// destination: 0 is arithmetically below every ceiling, and acting on that is how a
	// degrade lands on a rung heavier than the one it was escaping.
	Rendition media.Rendition

	// Read is how the source is fetched, and it is a value the readers RENDER rather than
	// a set of flags: both readers of one attempt open the upstream on identical terms
	// because they are handed the same policy.
	Read read.Policy

	// Decode is the axes this attempt must decode rather than copy, over and above
	// whatever the containers it writes are known not to carry. It is empty on a first
	// attempt and only ever gains axes within one link, which is what makes it a strict
	// descent: a copy this cast has proved fatal is not reinstated.
	//
	// It is a term of the ATTEMPT and not of a leg because both readers of a cast are
	// bound by it: the buffered read is the process that carries the auto-inserted
	// bitstream filter into MPEG-TS and therefore the one that dies on a bitstream it
	// cannot resynchronise, and the encode that tails the buffer must not then copy the
	// same packets straight back out.
	Decode carriage.Axes

	// Delivery is the operator's say over the delivery axis, carried here rather than read
	// from configuration by whoever needs it. A recovery is allowed to change it, so a
	// stage reading the configured value would be reading the answer to a question that
	// has since been asked again.
	Delivery media.DeliveryPreference
}

// identity is what an attempt DOES, as opposed to which try of a cast it is: exactly the
// fields a strategy can change, and nothing a strategy cannot.
//
// It exists because the two things that read it used to spell it out separately, in different
// words, next to each other. One is the line a failed run is read in, the other is what the
// ledger tells a revised attempt from a repeat by, and the second carries a requirement: a
// strategy whose whole effect is invisible to it produces an attempt the ledger refuses as a
// repeat, so its recovery never runs at all. They had already drifted, the ledger's half
// having been written without the rung's height while the line beside it printed one, so a
// degrade onto a shorter rung at the same bitrate was a recovery that could not run.
//
// Both are rendered from this value, so extending the identity in one and forgetting it in
// the other is no longer something that can be done.
type identity struct {
	Candidate int
	URL       string
	Bitrate   media.Bitrate
	Height    int
	Read      string
	Delivery  media.DeliveryPreference
	Decode    carriage.Axes
}

// identify spells the link rather than dereferencing it, and fills in what an unset field
// means wherever the answer is a default rather than an absence. A strategy that returns an
// attempt with no source is a table bug to be refused like any other repeat, not a panic in
// the middle of a cast.
func (a Attempt) identify() identity {
	url := "none"
	if a.Source != nil && a.Source.URL != nil {
		url = a.Source.URL.String()
	}
	return identity{
		Candidate: a.Candidate,
		URL:       url,
		Bitrate:   a.Rendition.Bitrate,
		Height:    a.Rendition.Height,
		Read:      cmp.Or(a.Read.Name, "unset"),
		Delivery:  cmp.Or(a.Delivery, media.DeliveryAuto),
		Decode:    a.Decode,
	}
}

// String is the one line that says what is being tried, in the vocabulary a failed run
// has to be read in: which link of how many, which rung, on what read terms, copying
// what.
func (a Attempt) String() string {
	id := a.identify()
	return fmt.Sprintf("candidate=%d url=%s rung_bitrate=%d rung_height=%d read=%s delivery=%s decode=%s",
		id.Candidate, id.URL, id.Bitrate, id.Height, id.Read, id.Delivery, id.Decode)
}

// key is that identity as one comparable string, so the ledger can tell a revised attempt
// from one this cast has already run. Try is deliberately no part of it: two attempts that
// read the same link the same way are the same attempt however many tries apart they are.
//
// It renders the whole struct rather than a list of its fields chosen here, which is the
// point: a field added to the identity is in the ledger's answer the moment it exists, with
// nothing for anybody to remember.
func (a Attempt) key() string {
	return fmt.Sprintf("%+v", a.identify())
}
