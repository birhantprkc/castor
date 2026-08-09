package read

import (
	"slices"
	"strings"
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

// TestEveryShapeOfSourceMatchesARow is the coverage guard the table's last row being a
// predicate rather than a default makes necessary. A shape that falls through returns a
// zero Policy, which renders no deadline, no reconnection and no pace at all, so the
// combination nobody thought of must be an error at selection rather than the most
// reckless read castor can perform.
func TestEveryShapeOfSourceMatchesARow(t *testing.T) {
	for _, segmented := range []bool{false, true} {
		for _, live := range []bool{false, true} {
			for _, framing := range framings {
				shape := Shape{Segmented: segmented, Framing: framing, Live: live}
				policy, err := For(shape, configuredDeadline)
				if err != nil {
					t.Errorf("For(%s) = %v, want a row", shape, err)
					continue
				}
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
		name:  "segments whose framing no document stated are not treated as fragile",
		shape: Shape{Segmented: true, Framing: media.FramingUnknown},
		want:  "segment-in-band",
	}, {
		name:  "a source castor read no document for is one long GET",
		shape: Shape{},
		want:  "whole-file",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := For(tt.shape, configuredDeadline)
			if err != nil {
				t.Fatal(err)
			}
			if policy.Name != tt.want {
				t.Errorf("For(%s) chose %q, want %q", tt.shape, policy.Name, tt.want)
			}
		})
	}
}

// TestOnlyAFragileSourceIsReadWithNoMidReadDeadline is the whole of what arming the
// fragile row changed, stated over every shape a source can have: the configured duration
// reaches every read castor makes except the one where firing it corrupts the stream.
//
// The fragile shape is EXACTLY the one where an abandoned read is unrecoverable (segmented,
// fMP4, and not a live edge, which is read on terms of its own), so the property is written
// as an exception of one rather than as a lookup of the row name: a row that started
// withholding the deadline from MPEG-TS segments, or from one long GET, would be trading a
// noisy failure for a silent hang on shapes where the deadline costs a retry and nothing
// more.
func TestOnlyAFragileSourceIsReadWithNoMidReadDeadline(t *testing.T) {
	for _, segmented := range []bool{false, true} {
		for _, live := range []bool{false, true} {
			for _, framing := range framings {
				shape := Shape{Segmented: segmented, Framing: framing, Live: live}
				policy, err := For(shape, configuredDeadline)
				if err != nil {
					t.Fatal(err)
				}
				fragile := segmented && framing == media.FramingOutOfBand && !live
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
		policy, err := For(shape, configuredDeadline)
		if err != nil {
			t.Fatal(err)
		}
		if policy.SegmentRetries != segmentOpenRetries {
			t.Errorf("For(%s) re-fetches a failed segment open %d times (row %q), want the derived %d",
				shape, policy.SegmentRetries, policy.Name, segmentOpenRetries)
		}
	}

	whole, err := For(Shape{}, configuredDeadline)
	if err != nil {
		t.Fatal(err)
	}
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
	for _, r := range policies {
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
	live, err := For(Shape{Segmented: true, Live: true}, configuredDeadline)
	if err != nil {
		t.Fatal(err)
	}
	if live.Pace.Realtime != 1.0 || live.Pace.Burst != 0 {
		t.Errorf("a live edge is read at %+v, want realtime with no burst", live.Pace)
	}

	for _, shape := range []Shape{{}, {Segmented: true}, {Segmented: true, Framing: media.FramingOutOfBand}} {
		policy, err := For(shape, configuredDeadline)
		if err != nil {
			t.Fatal(err)
		}
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

// TestAShapeNoRowAnswersIsNamed covers the failure mode the last row's predicate
// creates. It walks the table directly rather than through For, because the point is
// that For reports rather than answers, and with the shipped rows every shape matches.
func TestAShapeNoRowAnswersIsNamed(t *testing.T) {
	shape := Shape{Segmented: true, Framing: media.FramingOutOfBand}
	if matched := slices.ContainsFunc(policies, func(r rule) bool { return r.when(shape) }); !matched {
		t.Fatalf("the shipped table no longer answers %s, so this test is measuring the wrong thing", shape)
	}

	// The unmatched path, over a table that deliberately answers nothing.
	policy, err := selectFrom([]rule{{name: "never", when: func(Shape) bool { return false }}}, shape, configuredDeadline)
	if err == nil {
		t.Fatalf("a shape no row answers produced %+v, and a zero policy reads with no deadline, no reconnection and no pace", policy)
	}
	if !strings.Contains(err.Error(), shape.String()) {
		t.Errorf("the error is %q, which does not name the shape %q anyone would write the missing row from", err, shape)
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
			was, err := For(shape, configuredDeadline)
			if err != nil {
				t.Fatal(err)
			}
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
			if shape.Segmented && shape.Framing == media.FramingOutOfBand && got.Deadline != 0 {
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
