package read

import (
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

const configuredDeadline = 37 * time.Second

func TestThePolicyASourceShapeIsReadWith(t *testing.T) {
	for _, tt := range []struct {
		shape    media.Fetch
		name     string
		deadline time.Duration
		pace     Pace
		retries  int
	}{
		{media.Fetch{Segmented: true, Framing: media.FramingOutOfBand, Live: true}, "live-edge", configuredDeadline, paceLive, segmentOpenRetries},
		{media.Fetch{Live: true}, "live-edge", configuredDeadline, paceLive, segmentOpenRetries},
		// Abandoning a fragment mid-read truncates what nothing can resynchronise.
		{media.Fetch{Segmented: true, Framing: media.FramingOutOfBand}, "segment-fragile", 0, paceVOD, segmentOpenRetries},
		{media.Fetch{Segmented: true, Framing: media.FramingUnknown}, "segment-fragile", 0, paceVOD, segmentOpenRetries},
		{media.Fetch{Segmented: true, Framing: media.FramingInBand}, "segment-in-band", configuredDeadline, paceVOD, segmentOpenRetries},
		// No plain-file demuxer accepts a segment retry flag.
		{media.Fetch{}, "whole-file", configuredDeadline, paceVOD, 0},
	} {
		t.Run(tt.shape.String(), func(t *testing.T) {
			p := For(tt.shape, configuredDeadline)
			if p.Name != tt.name || p.Deadline != tt.deadline || p.Pace != tt.pace || p.SegmentRetries != tt.retries {
				t.Errorf("For = %q deadline %s pace %+v retries %d, want %q deadline %s pace %+v retries %d",
					p.Name, p.Deadline, p.Pace, p.SegmentRetries, tt.name, tt.deadline, tt.pace, tt.retries)
			}
		})
	}
}

func TestACautiousReadGivesUpOnlyItsPace(t *testing.T) {
	for _, shape := range []media.Fetch{{Segmented: true, Framing: media.FramingOutOfBand}, {Segmented: true, Framing: media.FramingInBand}, {}} {
		t.Run(shape.String(), func(t *testing.T) {
			was := For(shape, configuredDeadline)
			got, ok := cautious(was)
			if !ok || got.Pace != pacePlayback {
				t.Fatalf("cautious = %+v (%v), want playback pace with no burst", got.Pace, ok)
			}
			if got.Deadline != was.Deadline || got.Backoff != was.Backoff || got.SegmentRetries != was.SegmentRetries || got.Name == was.Name {
				t.Errorf("relaxing changed more than the pace: %+v from %+v", got, was)
			}
			if _, again := cautious(got); again {
				t.Error("a cautious read offered a second relaxation")
			}
		})
	}
}
