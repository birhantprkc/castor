package read

import (
	"fmt"
	"time"

	"github.com/stupside/castor/internal/media"
)

// rule is one row of the read table: which shapes of source it answers, why, and the
// policy it answers with.
type rule struct {
	// name identifies the row in a log line, and is what a shape no row matched is
	// reported against.
	name string
	// why is the reasoning, carried out to the caller so a read that was deliberately
	// cautious says so where a reader can see it rather than only in this file.
	why string
	// when reports whether this row answers a source of this shape.
	when func(Shape) bool
	// read is the row's policy. It is a function of the configured mid-read deadline
	// because that duration is the one term in a read an operator still owns, and
	// because a row is free to refuse it: a fragment that must arrive whole cannot be
	// abandoned partway through on a timer.
	read func(deadline time.Duration) Policy
}

// policies is how castor fetches each shape of source, in order, first match. It is
// walked by For, which is the only way a policy is obtained.
//
// The rows are ordered from the most specific fact to the least, and the last row is
// a predicate rather than a default, so a shape that matches nothing is an error
// naming the shape instead of a silent set of flags nobody chose. Adding a rule is one
// row, or one field on an existing row: a CDN that answers a mid-stream segment with
// 503 rather than 429 is a wider RetryStatuses on the segmented rows, and a source
// that needs a different pace is a Pace. No flag list is edited to do either.
var policies = []rule{{
	name: "live-edge",
	why:  "a live edge arrives at 1x and cannot be outrun, and a burst against it only asks a CDN for segments that do not exist yet",
	when: func(s Shape) bool { return s.Live },
	read: func(deadline time.Duration) Policy {
		return Policy{Deadline: deadline, Backoff: BackoffMax, RetryStatuses: rateLimited, Pace: paceLive}
	},
}, {
	// The one shape where the mid-read deadline is the hazard rather than the
	// protection. Abandoning an fMP4 fragment partway through truncates it, a truncated
	// AVCC stream desyncs the h264_mp4toannexb filter that a copy into MPEG-TS cannot
	// do without, and the cast then dies deep into a title with a bitstream error and
	// no way back. The deadline it carries is still the configured one, because
	// withholding it hands the stall it guards to a judgement that can name it and
	// castor does not yet make one.
	//
	// A LIVE fMP4 edge does not reach here: the row above answers it, which is right
	// while both rows read alike and is the first thing to revisit when they stop.
	name: "segment-fragile",
	why:  "the segments are fMP4 (the playlist declares a Media Initialization Section), so a read abandoned mid-fragment truncates a fragment nothing downstream can resynchronise",
	when: func(s Shape) bool { return s.Segmented && s.Framing == media.FramingOutOfBand },
	read: func(deadline time.Duration) Policy {
		return Policy{Deadline: deadline, Backoff: BackoffMax, RetryStatuses: rateLimited, Pace: paceVOD}
	},
}, {
	// MPEG-TS segments, or segments whose framing no document stated. A read abandoned
	// mid-segment costs a retry here and nothing more, because every frame carries its
	// own configuration, so the deadline is the cheapest way to notice a tarpit.
	name: "segment-in-band",
	why:  "the segments carry their configuration in band, so an abandoned read costs a retry rather than a stream nothing can resynchronise",
	when: func(s Shape) bool { return s.Segmented },
	read: func(deadline time.Duration) Policy {
		return Policy{Deadline: deadline, Backoff: BackoffMax, RetryStatuses: rateLimited, Pace: paceVOD}
	},
}, {
	// One long GET. Nothing throttles it, and the deadline is the only way a connection
	// that was accepted and then went quiet is ever noticed: this is the duration the
	// configuration knob was written for.
	name: "whole-file",
	why:  "one long GET, where a stalled read is the only symptom a tarpit has",
	when: func(s Shape) bool { return !s.Segmented },
	read: func(deadline time.Duration) Policy {
		return Policy{Deadline: deadline, Backoff: BackoffMax, RetryStatuses: rateLimited, Pace: paceVOD}
	},
}}

// For selects the policy a source of this shape is read with. deadline is the
// configured mid-read timeout, which rows are free to apply or to withhold.
//
// A shape no row answers is an error rather than a bare Policy, and the error names
// the shape: a zero Policy renders no deadline, no reconnection and no pacing, which
// is the most dangerous read castor can perform and the last thing a missing row
// should silently produce.
func For(s Shape, deadline time.Duration) (Policy, error) {
	return selectFrom(policies, s, deadline)
}

// Cautious is the same source asked for at the pace a source that has already stopped
// delivering gets: exactly playback speed, with no wire-speed burst. It reports false
// when there is nothing left to give up, which is what bounds it to one step.
//
// It is the one thing castor can change about HOW a link is read that a stall gives any
// reason to change. The burst is a deliberate demand for as many segments as the wire
// will carry for the first ninety seconds of every VOD read, and a burst of requests
// behind one signature against exactly these hosts is castor's known way of earning a
// 429: it is why the ranker caps its probes per host, why a live edge is never bursted,
// and why the retry set pairs 429 with a full minute of backoff instead of letting the
// HLS demuxer burn through segment numbers that all fail and keep the address
// tarpitted. A source that went silent for two reconnect ceilings while castor was
// bursting at it is the shape that describes, so the retry is asked for politely.
//
// Nothing else about the read is touched. The deadline, the backoff ceiling and the
// retry statuses are what the source's SHAPE called for, and a stall is no evidence
// against any of them: withholding a deadline on a fragile source is not a thing to
// reconsider because the fragile source stalled.
//
// The pace it drops to is paceLive's, not a number of its own: "the pace of a source
// that cannot be outrun" is what castor is asking for when it stops trying to run
// ahead, and there is one such pace in the program.
func Cautious(p Policy) (Policy, bool) {
	if p.Pace == paceLive {
		return p, false
	}
	p.Name = "cautious"
	p.Why = "this link already stopped delivering once, so it is asked for at playback pace with no wire-speed burst rather than raced"
	p.Pace = paceLive
	return p, true
}

// selectFrom is For's walk over an explicit table, so the unmatched outcome is
// reachable from a test without editing the shipped rows out from under the caller.
func selectFrom(rules []rule, s Shape, deadline time.Duration) (Policy, error) {
	for _, r := range rules {
		if !r.when(s) {
			continue
		}
		p := r.read(deadline)
		p.Name, p.Why = r.name, r.why
		return p, nil
	}
	return Policy{}, fmt.Errorf("no read policy for a source with %s", s)
}
