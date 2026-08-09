package read

import (
	"time"

	"github.com/stupside/castor/internal/media"
)

// rule is one row of the read table: which shapes of source it answers, why, and the
// policy it answers with.
type rule struct {
	// name identifies the row in a log line.
	name string
	// why is the reasoning, carried out to the caller so a read that was deliberately
	// cautious says so where a reader can see it rather than only in this file.
	why string
	// when reports whether this row answers a source of this shape. It is nil on the total
	// row, which is never asked: see table.
	when func(Shape) bool
	// read is the row's policy. It is a function of the configured mid-read deadline
	// because that duration is the one term in a read an operator still owns, and
	// because a row is free to refuse it: a fragment that must arrive whole cannot be
	// abandoned partway through on a timer.
	read func(deadline time.Duration) Policy
}

// table is an ordered set of rows plus the row that answers whatever they did not. The
// total row carries no predicate at all, which is what makes For total by construction
// rather than by a `return true` at the bottom of a slice: there is no walk to fall off
// the end of, so no caller carries an error for a shape that cannot exist.
type table struct {
	rules []rule
	total rule
}

// policies is how castor fetches each shape of source, in order, first match. It is
// walked by For, which is the only way a policy is obtained.
//
// The rows are ordered from the most specific fact to the least, and what is left over
// (a source no playlist described, so one long GET) is the total row. Adding a rule is one
// row, or one field on an existing row: a CDN that answers a mid-stream segment with a
// status nothing retries is a wider RetryStatuses on the segmented rows, and a source
// that needs a different pace is a Pace. No flag list is edited to do either.
var policies = table{rules: []rule{{
	name: "live-edge",
	why:  "a live edge arrives at 1x and cannot be outrun, and a burst against it only asks a CDN for segments that do not exist yet",
	when: func(s Shape) bool { return s.Live },
	read: func(deadline time.Duration) Policy {
		return Policy{
			Deadline:       deadline,
			Backoff:        BackoffMax,
			RetryStatuses:  transient,
			SegmentRetries: segmentOpenRetries,
			Pace:           paceLive,
		}
	},
}, {
	// THE ONE SHAPE WHERE THE MID-READ DEADLINE IS THE HAZARD AND NOT THE PROTECTION, so
	// it is the one row that withholds it.
	//
	// A -rw_timeout that fires partway through an fMP4 fragment makes ffmpeg's HLS demuxer
	// abandon that fragment and jump to the next segment. The AVCC stream it hands on is
	// then truncated mid-NAL, which desynchronises the h264_mp4toannexb filter a copy into
	// MPEG-TS cannot do without: it reads the next four bytes as a length, prints "Invalid
	// NAL unit size (-1140850681 > 97253)" and the reader dies at exit 183. That killed a
	// cast at minute forty, with a viewer watching and nothing to be done about it (a
	// renderer already holds the URL, so the attempt is not revisable, and the fragment is
	// gone either way).
	//
	// SegmentRetries does not cover it and cannot be raised until it does. That budget is
	// spent on a segment whose OPEN failed, which is a different event with a different
	// symptom: the failed open prints "Segment N of playlist 0 failed too many times,
	// skipping" while the mid-read timeout is a silent jump with no line at all.
	//
	// WHAT BOUNDS THE SILENCE INSTEAD, since a read with no deadline can wait on a tarpitted
	// socket forever: castor's own stall judgement over the bytes this read has landed
	// (watch's "stalled" row, at two reconnect ceilings plus a margin). It reaches this read
	// before playback through the playback gate and in flight through the playing cast, so
	// the trade is the configured deadline's patience (thirty seconds as shipped) for two and
	// a half minutes of it, in exchange for a fragment nothing can resynchronise never being
	// manufactured. The judgement is also the only one of the two that can NAME what it gave
	// up on, where -rw_timeout's answer to the same silence was a corrupt bitstream and a
	// filter error blaming the codec.
	//
	// A LIVE fMP4 edge does not reach here: the row above answers it and keeps the deadline,
	// so it keeps this exposure. That boundary is stated rather than argued, because the
	// trade is not the same one: a live edge is read at exactly 1x with no burst, so the read
	// holds no lead to spend waiting, and the fragment it is stalled on rolls out of the live
	// window while it waits. Both answers end that cast, and nothing observed says which
	// ends it better.
	//
	// THE PREDICATE IS AN ABSENCE OF PERMISSION, NOT A PRESENCE OF FRAGILITY, and that is the
	// whole of why it reads `!= FramingInBand` rather than `== FramingOutOfBand`. Framing is
	// unknown whenever the chosen variant's own playlist could not be fetched, which is a
	// reachable outcome and not a theoretical one (resolve/program.go stops describing the
	// origin there and hands back what the master already said). An fMP4 master whose variant
	// document went missing is still fMP4; keying on the positive fact routed it to the row
	// below, whose reasoning asserts in-band framing as established, and handed it the exact
	// deadline that manufactures the exit-183 truncation this row exists to prevent. Only a
	// document that positively SAID in-band earns the mid-read deadline.
	name: "segment-fragile",
	why:  "nothing established that these segments carry their configuration in band (an fMP4 playlist declares a Media Initialization Section, and an unread variant playlist declares nothing at all), so a read abandoned mid-fragment may truncate a fragment nothing downstream can resynchronise, and no mid-read deadline is applied at all",
	when: func(s Shape) bool { return s.Segmented && s.Framing != media.FramingInBand },
	read: func(time.Duration) Policy {
		return Policy{
			Backoff:        BackoffMax,
			RetryStatuses:  transient,
			SegmentRetries: segmentOpenRetries,
			Pace:           paceVOD,
		}
	},
}, {
	// MPEG-TS segments, and nothing else: a playlist castor read and found no Media
	// Initialization Section in. A read abandoned mid-segment costs a retry here and nothing
	// more, because every frame carries its own configuration, so the deadline is the
	// cheapest way to notice a tarpit. The predicate is a bare s.Segmented only because the
	// row above already took every segmented shape whose framing is not established fact;
	// what reaches here has a document behind it saying so.
	name: "segment-in-band",
	why:  "the segments carry their configuration in band, so an abandoned read costs a retry rather than a stream nothing can resynchronise",
	when: func(s Shape) bool { return s.Segmented },
	read: func(deadline time.Duration) Policy {
		return Policy{
			Deadline:       deadline,
			Backoff:        BackoffMax,
			RetryStatuses:  transient,
			SegmentRetries: segmentOpenRetries,
			Pace:           paceVOD,
		}
	},
}}, total: rule{
	// One long GET, and the total row: every segmented shape is answered above, so what
	// reaches here is a source no playlist described. Nothing throttles it, and the deadline
	// is the only way a connection that was accepted and then went quiet is ever noticed:
	// this is the duration the configuration knob was written for, and this is the row it
	// survives untouched on.
	//
	// No segment retry budget, because there are no segments to re-fetch: a dropped read is
	// answered by the reconnect terms above and by nothing else.
	name: "whole-file",
	why:  "one long GET, where a stalled read is the only symptom a tarpit has",
	read: func(deadline time.Duration) Policy {
		return Policy{Deadline: deadline, Backoff: BackoffMax, RetryStatuses: transient, Pace: paceVOD}
	},
}}

// For selects the policy a source of this shape is read with. deadline is the
// configured mid-read timeout, which rows are free to apply or to withhold.
//
// It cannot fail. A zero Policy renders no deadline, no reconnection and no pacing, which
// is the most dangerous read castor can perform, so the table answers with its total row
// rather than leaving a caller to decide what an unanswered shape means.
func For(s Shape, deadline time.Duration) Policy {
	r, ok := selectFrom(policies.rules, s)
	if !ok {
		r = policies.total
	}
	p := r.read(deadline)
	p.Name, p.Why = r.name, r.why
	return p
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

// selectFrom is the ordered half of For's walk, over an explicit slice of rows so the
// unmatched outcome is reachable from a test without editing the shipped rows out from
// under the caller. Production never sees it: the total row is what For answers with.
func selectFrom(rules []rule, s Shape) (rule, bool) {
	for _, r := range rules {
		if r.when(s) {
			return r, true
		}
	}
	return rule{}, false
}
