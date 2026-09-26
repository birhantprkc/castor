package read

import (
	"slices"
	"time"

	"github.com/stupside/castor/internal/media"
)

// rule is one row of the read table: which shapes of source it answers, why, and the policy it answers with.
type rule struct {
	name string
	why  string
	// when is nil on the total row, which is never asked: see table.
	when func(media.Fetch) bool
	read func(deadline time.Duration) Policy
}

// table is an ordered set of rows plus the row that answers whatever they did not.
type table struct {
	rules []rule
	total rule
}

var policies = table{rules: []rule{{
	name: "live-edge",
	why:  "a live edge arrives at 1x and cannot be outrun, so the read keeps its pace and wins back at the VOD rate whatever a stall cost it",
	when: func(f media.Fetch) bool { return f.Live },
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
	name: "segment-fragile",
	why:  "nothing established that these segments carry their configuration in band (an fMP4 playlist declares a Media Initialization Section, and an unread variant playlist declares nothing at all), so a read abandoned mid-fragment may truncate a fragment nothing downstream can resynchronise, and no mid-read deadline is applied at all",
	when: func(f media.Fetch) bool { return f.Segmented && f.Framing != media.FramingInBand },
	read: func(time.Duration) Policy {
		return Policy{
			Backoff:        BackoffMax,
			RetryStatuses:  transient,
			SegmentRetries: segmentOpenRetries,
			Pace:           paceVOD,
		}
	},
}, {
	name: "segment-in-band",
	why:  "the segments carry their configuration in band, so an abandoned read costs a retry rather than a stream nothing can resynchronise",
	when: func(f media.Fetch) bool { return f.Segmented },
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
	// One long GET, and the total row: what reaches here is a source no playlist described.
	name: "whole-file",
	why:  "one long GET, where a stalled read is the only symptom a tarpit has",
	read: func(deadline time.Duration) Policy {
		return Policy{Deadline: deadline, Backoff: BackoffMax, RetryStatuses: transient, Pace: paceVOD}
	},
}}

// For selects the policy a source of this shape is read with.
func For(f media.Fetch, deadline time.Duration) Policy {
	for _, row := range policies.rules {
		if row.when(f) {
			return row.policy(deadline)
		}
	}
	return policies.total.policy(deadline)
}

// policy owns its retry statuses, so no caller aliases the table's shared slice.
func (r rule) policy(deadline time.Duration) Policy {
	p := r.read(deadline)
	p.Name, p.Why = r.name, r.why
	p.RetryStatuses = slices.Clone(p.RetryStatuses)
	return p
}

func cautious(p Policy) (Policy, bool) {
	// A live read held to 1x would fall behind for good, and one already at playback pace has nothing left to give.
	if p.Pace == paceLive || p.Pace == pacePlayback {
		return p, false
	}
	p.Name = "cautious"
	p.Why = "this link already stopped delivering once, so it is asked for at playback pace with no wire-speed burst rather than raced"
	p.Pace = pacePlayback
	return p, true
}
