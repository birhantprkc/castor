package attempt

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/stupside/castor/internal/cast/policy/compose"
	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// Intent is what a cast has; immutable during run, so attempt count is inherent.
type Intent struct {
	// Candidates: ranked by source.Ranker; each is resolved when an attempt first reaches it.
	Candidates []*source.Candidate

	// Deadline is the mid-read stall bound every read plan is derived with.
	Deadline time.Duration

	// Delivery: operator's answer; seeds first attempt, may be changed by recovery.
	Delivery compose.DeliveryPreference
}

// Attempt is one fully decided try: link, rung, fetch terms, delivery preference.
type Attempt struct {
	// Try counts within cast from 1; without it, different tries look identical.
	Try int

	// Candidate is an index into Intent.Candidates. Program is the resolved graph read for it.
	Candidate int
	Program   media.Program

	// Origin travels with candidate; one link's facts don't apply to another's.
	Origin source.Origin

	// Rendition: Program's selected rung; zero is absence of evidence, not free rendition.
	Rendition source.Rendition

	// Read: how inputs are fetched; value not flags, so both readers use identical terms.
	Read read.Plan

	// Decode: axes to decode not copy; only gains, never reinstates (strict descent).
	Decode media.Axes

	// Delivery: carried here not from config; recovery may change it.
	Delivery compose.DeliveryPreference
}

// identity is exactly the fields a strategy can change; used by both log and ledger.
type identity struct {
	Candidate int
	Program   string
	Bitrate   media.Bitrate
	Height    int
	Read      string
	Delivery  compose.DeliveryPreference
	Decode    media.Axes
}

// identify fills defaults for unset fields; treats missing source as table bug.
func (a Attempt) identify(redactURLSecrets bool) identity {
	return identity{
		Candidate: a.Candidate,
		Program:   programIdentity(a.Program, redactURLSecrets),
		Bitrate:   a.Rendition.Bitrate,
		Height:    a.Rendition.Height,
		Read:      cmp.Or(a.Read.String(), "unset"),
		Delivery:  cmp.Or(a.Delivery, compose.DeliveryAuto),
		Decode:    a.Decode,
	}
}

// String is the only rendering; add fields above or they're missing from ledger.
func (id identity) String() string {
	return fmt.Sprintf("candidate=%d program=%s rung_bitrate=%d rung_height=%d read=%s delivery=%s decode=%s",
		id.Candidate, id.Program, id.Bitrate, id.Height, id.Read, id.Delivery, id.Decode)
}

// String redacts the URL secrets, because a line a user reads is not a place to print a signed query.
func (a Attempt) String() string { return a.identify(true).String() }

// programIdentity includes all inputs/tracks; visible in ledger like primary URL changes.
func programIdentity(program media.Program, redactURLSecrets bool) string {
	parts := make([]string, 0, len(program.Inputs)+len(program.Tracks)+len(program.Offsets)+1)
	for _, input := range program.Inputs {
		inputURL := input.URL.String()
		if redactURLSecrets {
			u := *input.URL
			u.User, u.RawQuery, u.Fragment = nil, "", ""
			inputURL = u.String()
		}
		parts = append(parts, fmt.Sprintf("input:%s=%s:%s:%t",
			input.ID, inputURL, input.ContentType, input.RequiresRelaxedInput))
	}
	for _, track := range program.Tracks {
		parts = append(parts, fmt.Sprintf("track:%s=%s:%d:%t",
			track.Kind, track.Input, track.Index, track.Optional))
	}
	offsets := make([]string, 0, len(program.Offsets))
	for input, offset := range program.Offsets {
		offsets = append(offsets, fmt.Sprintf("%s=%s", input, offset))
	}
	slices.Sort(offsets)
	parts = append(parts, fmt.Sprintf("sync:%s:%s:%s",
		program.ClockInput, program.EndPolicy, strings.Join(offsets, ",")))
	return strings.Join(parts, "|")
}

// key is identity as string; Try omitted (same link/terms = same attempt).
func (a Attempt) key() string { return a.identify(false).String() }

// reading binds a resolution to the attempt with a read plan derived for its program.
func (a Attempt) reading(r source.Resolution, deadline time.Duration) Attempt {
	a.Program, a.Origin, a.Rendition = r.Program, r.Origin, r.Rendition
	a.Read = read.ForProgram(a.Program, deadline)
	return a
}

// SelfFetchHeight is the tallest picture a renderer fetching this attempt's link could pull.
func (a Attempt) SelfFetchHeight() int {
	return source.SelfFetchHeight(a.Program, a.Origin, a.Rendition)
}
