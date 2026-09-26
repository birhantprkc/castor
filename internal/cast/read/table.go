package read

import (
	"time"

	"github.com/stupside/castor/internal/media"
)

// rule is one row of the read table: which shapes of source it answers, why, and the policy it answers with.
type rule struct {
	name string
	why  string
	when func(media.Fetch) bool
	// policy is the row's answer; every read but one abandoned mid-fragment is also given the caller's deadline.
	policy     Policy
	noDeadline bool
}

var rules = []rule{{
	name:   "live-edge",
	why:    "a live edge arrives at 1x and cannot be outrun, so the read keeps its pace and wins back at the VOD rate whatever a stall cost it",
	when:   func(f media.Fetch) bool { return f.Live },
	policy: Policy{SegmentRetries: segmentOpenRetries, Pace: paceLive},
}, {
	name:       "segment-fragile",
	why:        "nothing established that these segments carry their configuration in band (an fMP4 playlist declares a Media Initialization Section, and an unread variant playlist declares nothing at all), so a read abandoned mid-fragment may truncate a fragment nothing downstream can resynchronise, and no mid-read deadline is applied at all",
	when:       func(f media.Fetch) bool { return f.Segmented && f.Framing != media.FramingInBand },
	policy:     Policy{SegmentRetries: segmentOpenRetries, Pace: paceVOD},
	noDeadline: true,
}, {
	name:   "segment-in-band",
	why:    "the segments carry their configuration in band, so an abandoned read costs a retry rather than a stream nothing can resynchronise",
	when:   func(f media.Fetch) bool { return f.Segmented },
	policy: Policy{SegmentRetries: segmentOpenRetries, Pace: paceVOD},
}}

// wholeFile answers what no row did: a source no playlist described, read as one long GET.
var wholeFile = rule{
	name:   "whole-file",
	why:    "one long GET, where a stalled read is the only symptom a tarpit has",
	policy: Policy{Pace: paceVOD},
}

// For selects the policy a source of this shape is read with.
func For(f media.Fetch, deadline time.Duration) Policy {
	row := wholeFile
	for _, r := range rules {
		if r.when(f) {
			row = r
			break
		}
	}
	p := row.policy
	p.Name, p.Why = row.name, row.why
	if !row.noDeadline {
		p.Deadline = deadline
	}
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
