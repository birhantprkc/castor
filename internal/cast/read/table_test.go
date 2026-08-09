package read

import (
	"slices"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

// configuredDeadline stands in for the operator's rw_timeout. It is a value no row
// could produce by accident, so a row that dropped it on the floor is visible rather
// than plausible.
const configuredDeadline = 37 * time.Second

// framings is every framing a source can be reported with. It is spelled out rather
// than derived because a new value added to media without a row here is exactly the
// gap this file exists to catch, and a loop over a list that grew itself would catch
// nothing.
var framings = []media.Framing{media.FramingUnknown, media.FramingInBand, media.FramingOutOfBand}

// TestEveryShapeOfSourceMatchesARow walks every shape a source can have against the
// table. A shape that fell through would be read on a zero Policy, which renders no
// deadline, no reconnection and no pace at all, so what this pins is that the answer is
// always a named row: the total row exists so that the combination nobody thought of is
// read on the careful terms rather than the most reckless ones castor can produce.
func TestEveryShapeOfSourceMatchesARow(t *testing.T) {
	for _, segmented := range []bool{false, true} {
		for _, live := range []bool{false, true} {
			for _, framing := range framings {
				shape := Shape{Segmented: segmented, Framing: framing, Live: live}
				policy := For(shape, configuredDeadline)
				if policy.Name == "" || policy.Why == "" {
					t.Errorf("For(%s) answered with an unnamed policy (%+v); the table fills both in so a row cannot mislabel itself", shape, policy)
				}
			}
		}
	}
}

// TestTheShapeChoosesThePolicy pins which row answers which source, because the rows
// are ordered and first match means the order IS the rule.
func TestTheShapeChoosesThePolicy(t *testing.T) {
	tests := []struct {
		name  string
		shape Shape
		want  string
	}{{
		name:  "a live edge is a live edge whatever frames its segments",
		shape: Shape{Segmented: true, Framing: media.FramingOutOfBand, Live: true},
		want:  "live-edge",
	}, {
		name:  "a live whole file cannot be outrun either",
		shape: Shape{Live: true},
		want:  "live-edge",
	}, {
		name:  "fMP4 segments are the fragile shape",
		shape: Shape{Segmented: true, Framing: media.FramingOutOfBand},
		want:  "segment-fragile",
	}, {
		name:  "MPEG-TS segments are not fragile",
		shape: Shape{Segmented: true, Framing: media.FramingInBand},
		want:  "segment-in-band",
	}, {
		// This row previously wanted "segment-in-band" and it was pinning the defect: a
		// segmented source whose variant playlist castor could not fetch is reported with
		// FramingUnknown, and routing it to the in-band row both asserts a fact nothing
		// established and hands an fMP4 program the mid-read deadline that truncates a
		// fragment at exit 183. Unknown framing is answered by the row that assumes nothing.
		name:  "segments whose framing no document stated are read as though they were fragile",
		shape: Shape{Segmented: true, Framing: media.FramingUnknown},
		want:  "segment-fragile",
	}, {
		name:  "a source castor read no document for is one long GET",
		shape: Shape{},
		want:  "whole-file",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := For(tt.shape, configuredDeadline)
			if policy.Name != tt.want {
				t.Errorf("For(%s) chose %q, want %q", tt.shape, policy.Name, tt.want)
			}
		})
	}
}

// TestOnlyAFragileSourceIsReadWithNoMidReadDeadline is the whole of what arming the
// fragile row changed, stated over every shape a source can have: the configured duration
// reaches every read castor makes except the ones where firing it can corrupt the stream.
//
// The property is written as an exception rather than as a lookup of the row name, so that
// a row which started withholding the deadline from a source castor reads as one long GET
// would be caught trading a noisy failure for a silent hang.
//
// The exception is stated as an ABSENCE of in-band framing on a VOD segmented read, which
// is the arming this stage did: a segmented source whose variant playlist could not be
// fetched carries FramingUnknown, and it is neither established to be safe to abandon
// mid-read nor distinguishable from the fMP4 program that desynced h264_mp4toannexb and
// ended a cast at minute forty. A live edge keeps the deadline whatever frames it, because
// the row above answers it on terms of its own.
func TestOnlyAFragileSourceIsReadWithNoMidReadDeadline(t *testing.T) {
	for _, segmented := range []bool{false, true} {
		for _, live := range []bool{false, true} {
			for _, framing := range framings {
				shape := Shape{Segmented: segmented, Framing: framing, Live: live}
				policy := For(shape, configuredDeadline)
				fragile := segmented && framing != media.FramingInBand && !live
				want := configuredDeadline
				if fragile {
					want = 0
				}
				if policy.Deadline != want {
					t.Errorf("For(%s) reads with a %s deadline (row %q), want %s", shape, policy.Deadline, policy.Name, want)
				}
			}
		}
	}
}

// TestASegmentedSourceNoPlaylistDescribedIsNotHandedTheDeadline states the reachable path
// on its own, because it is the one the cross-product above would keep passing on if the
// fragile row went back to keying on the positive fact and something else started
// withholding the deadline.
//
// The path: the master playlist named the variants, the chosen variant's own GET failed, so
// resolution stops describing the origin and Framing is left unknown while Segmented stands.
// That source may be an fMP4 program, and a -rw_timeout that fires partway through an fMP4
// fragment is what manufactures the truncated AVCC stream nothing downstream resynchronises.
// Castor's own stall judgement is what bounds the silence instead, on the same terms the
// fragile row already argues.
func TestASegmentedSourceNoPlaylistDescribedIsNotHandedTheDeadline(t *testing.T) {
	policy := For(Shape{Segmented: true}, configuredDeadline)
	if policy.Name != "segment-fragile" {
		t.Errorf("a segmented source whose variant playlist castor could not read is answered by %q (%s), want the row that assumes nothing about its framing",
			policy.Name, policy.Why)
	}
	if policy.Deadline != 0 {
		t.Errorf("it is read with a %s mid-read deadline, which is the timer that abandons an fMP4 fragment mid-body; nothing established that these segments carry their configuration in band", policy.Deadline)
	}
}

// TestASegmentedReadRefetchesASegmentWhoseOpenFailed pins the term that covers the failure
// the withheld deadline does NOT: a segment castor never got a byte of. Left at ffmpeg's
// own zero, the first failed open prints "Segment N of playlist 0 failed too many times,
// skipping" and the fragment is simply gone, which on an fMP4 program is a hole no
// downstream copy can fill.
//
// Every row that fetches segments carries it, and the row that does not fetch segments
// carries none: a budget rendered for a source that is not a playlist is an option ffmpeg's
// plain-file demuxers do not have, and it aborts the read rather than being ignored.
func TestASegmentedReadRefetchesASegmentWhoseOpenFailed(t *testing.T) {
	for _, shape := range []Shape{
		{Segmented: true, Framing: media.FramingOutOfBand},
		{Segmented: true, Framing: media.FramingInBand},
		{Segmented: true, Framing: media.FramingUnknown},
		{Segmented: true, Live: true},
	} {
		policy := For(shape, configuredDeadline)
		if policy.SegmentRetries != segmentOpenRetries {
			t.Errorf("For(%s) re-fetches a failed segment open %d times (row %q), want the derived %d",
				shape, policy.SegmentRetries, policy.Name, segmentOpenRetries)
		}
	}

	whole := For(Shape{}, configuredDeadline)
	if whole.SegmentRetries != 0 {
		t.Errorf("one long GET carries a segment retry budget of %d, though it fetches no segments and the flag is one no plain-file demuxer accepts", whole.SegmentRetries)
	}
}

// TestEveryPolicyCanWaitOutATransientOrigin pins the pair that has to travel together. A
// backoff ceiling with no status set to reconnect on lets an HLS demuxer burn through
// segment numbers that all answer 429, and a status set with no ceiling retries
// immediately into the same rate limiter. Neither half is useful alone, so no row may
// carry one without the other.
//
// The set is the transient class and not one code, because a CDN answering a mid-stream
// segment 503 has said exactly what one answering 429 said. What it may never carry is a
// refusal: 401, 403, 404 and 410 are answers about the request, so retrying one for a full
// ceiling spends a viewer's time reaching the conclusion the first answer already gave, and
// a spent signed link is the commonest of them.
func TestEveryPolicyCanWaitOutATransientOrigin(t *testing.T) {
	for _, r := range append(slices.Clone(policies.rules), policies.total) {
		policy := r.read(configuredDeadline)
		if policy.Backoff != BackoffMax {
			t.Errorf("row %q backs off for %s, want the shared ceiling of %s", r.name, policy.Backoff, BackoffMax)
		}
		for _, status := range []int{429, 500, 502, 503, 504} {
			if !slices.Contains(policy.RetryStatuses, status) {
				t.Errorf("row %q retries on %v, which does not include the transient %d", r.name, policy.RetryStatuses, status)
			}
		}
		for _, refusal := range []int{401, 403, 404, 410} {
			if slices.Contains(policy.RetryStatuses, refusal) {
				t.Errorf("row %q retries a %d, an answer about the request that no retry changes", r.name, refusal)
			}
		}
	}
}

// TestOnlyALiveSourceIsPacedAtRealtime pins the one axis the rows actually differ on:
// a live edge is read at wall-clock speed and everything else is allowed to run ahead
// of playback. The headroom is also what a deliverability judgement is measured
// against, so a row that paced a VOD source at 1.0 would make a starved link
// indistinguishable from a healthy live one.
func TestOnlyALiveSourceIsPacedAtRealtime(t *testing.T) {
	live := For(Shape{Segmented: true, Live: true}, configuredDeadline)
	if live.Pace.Realtime != 1.0 || live.Pace.Burst != 0 {
		t.Errorf("a live edge is read at %+v, want realtime with no burst", live.Pace)
	}

	for _, shape := range []Shape{{}, {Segmented: true}, {Segmented: true, Framing: media.FramingOutOfBand}} {
		policy := For(shape, configuredDeadline)
		if policy.Pace.Realtime <= 1.0 {
			t.Errorf("For(%s) is paced at %v, which leaves a reader no headroom over 1x playback", shape, policy.Pace.Realtime)
		}
		if policy.Pace.Burst <= 0 {
			t.Errorf("For(%s) is given no wire-speed burst, so a cast has nothing to prebuffer with", shape)
		}
	}
}

// TestTheShapeIsHarvestedAndNotDerived is the reason media.Origin.Framing and
// media.Origin.Live are collected at all: the read policy is the consumer they were
// harvested for, and a Shape built from anything else would discard them at the exact
// point they were meant to be used.
func TestTheShapeIsHarvestedAndNotDerived(t *testing.T) {
	origin := media.Origin{
		Renditions: []media.Rendition{{Bitrate: 18505000}},
		Segmented:  true,
		Framing:    media.FramingOutOfBand,
		Live:       true,
		Encrypted:  true,
		Duration:   107 * time.Minute,
	}
	want := Shape{Segmented: true, Framing: media.FramingOutOfBand, Live: true}
	if got := ShapeOf(origin); got != want {
		t.Errorf("ShapeOf(%+v) = %s, want %s", origin, got, want)
	}
	if got := ShapeOf(media.Origin{}); got != (Shape{}) {
		t.Errorf("ShapeOf of a source castor read no document for = %s, want every fact unknown", got)
	}
}

// TestTheOrderedRowsStopWhereTheTotalRowBegins is the totality guarantee in the form the
// type now carries it. The total row holds no predicate at all, so it cannot decline and
// For cannot fail; what needs pinning is that it really is held apart from the ordered
// rows, because a `when` on it would be a predicate the walk never asks and a shape it
// would silently refuse to answer.
//
// The unmatched outcome is reached over rows that deliberately answer nothing, because
// with the shipped table it is unreachable: that is what having a total row rather than a
// last predicate bought, and it is why For hands back a Policy instead of an error nobody
// can produce.
func TestTheOrderedRowsStopWhereTheTotalRowBegins(t *testing.T) {
	if policies.total.when != nil {
		t.Error("the total row carries a predicate the walk never asks: a row that can decline belongs in policies.rules, where the walk reads it")
	}
	if _, ok := selectFrom([]rule{{name: "never", when: func(Shape) bool { return false }}}, Shape{}); ok {
		t.Fatal("a table whose every row declines matched one anyway")
	}
	// The shape the ordered rows are written to leave over: not live, not segmented.
	if got := For(Shape{}, configuredDeadline); got.Name != policies.total.name {
		t.Errorf("a source no playlist described is read as %q, want the total row %q", got.Name, policies.total.name)
	}
}

// TestACautiousReadGivesUpOnlyWhatCouldBeThrottled is the one change to HOW a link is
// read that a stall gives any reason to make, and this pins what it may and may not
// touch.
//
// Every VOD read opens by demanding ninety seconds of stream as fast as the wire will
// carry it. That burst is castor's own known way of earning a rate limiter's silence
// against exactly these hosts, so it is what a source that already went quiet stops
// being asked for. The deadline, the backoff ceiling and the retry statuses are what the
// source's SHAPE called for, and a stall is no evidence against any of them: withholding
// a deadline from a source whose fragments must arrive whole is not a decision to revisit
// because that source stalled.
func TestACautiousReadGivesUpOnlyWhatCouldBeThrottled(t *testing.T) {
	for _, shape := range []Shape{
		{Segmented: true, Framing: media.FramingOutOfBand},
		{Segmented: true, Framing: media.FramingInBand},
		{},
	} {
		t.Run(shape.String(), func(t *testing.T) {
			was := For(shape, configuredDeadline)
			got, ok := Cautious(was)
			if !ok {
				t.Fatalf("a %s read has a wire-speed burst to give up", was.Name)
			}
			if got.Pace != paceLive {
				t.Errorf("cautious pace = %+v, want the pace of a source that cannot be outrun (%+v): there is one such pace in the program",
					got.Pace, paceLive)
			}
			if got.Deadline != was.Deadline || got.Backoff != was.Backoff ||
				!slices.Equal(got.RetryStatuses, was.RetryStatuses) || got.SegmentRetries != was.SegmentRetries {
				t.Errorf("relaxing changed the terms the source's shape called for: %+v, want the deadline %s, backoff %s, statuses %v and %d segment retries it had",
					got, was.Deadline, was.Backoff, was.RetryStatuses, was.SegmentRetries)
			}
			// Stated as a value rather than only as an equality, because on the fragile shape
			// both sides are zero and an equality alone passes for a relaxation that took a
			// withheld deadline and handed the read one.
			if shape.Segmented && shape.Framing != media.FramingInBand && got.Deadline != 0 {
				t.Errorf("a cautious read of a fragile source carries a %s mid-read deadline; a stall is no reason to start abandoning fragments partway through", got.Deadline)
			}
			if got.Name == was.Name || got.Name == "" || got.Why == "" {
				t.Errorf("the relaxed policy reports itself as %q, which the ledger cannot tell from the read that already failed", got.Name)
			}
		})
	}
}

// TestARelaxedReadHasNothingLeftToGiveUp is what bounds the recovery built on this to one
// step per link: the pace it drops to is the floor, so asking twice answers false and a
// loop cannot spend a viewer's time reading the same link the same way.
func TestARelaxedReadHasNothingLeftToGiveUp(t *testing.T) {
	once, ok := Cautious(Policy{Name: "whole-file", Pace: paceVOD})
	if !ok {
		t.Fatal("a VOD pace has a burst to give up")
	}
	if _, ok := Cautious(once); ok {
		t.Error("a cautious read was relaxed again, so nothing bounds how often a link can be re-read on the same terms")
	}
	// A live edge is already there, which is the same statement made about the source
	// rather than about a previous attempt: it cannot be outrun, so there was never
	// anything to stop asking for.
	if _, ok := Cautious(Policy{Name: "live-edge", Pace: paceLive}); ok {
		t.Error("a live edge was offered a relaxation, though it is already read at the pace this produces")
	}
}
