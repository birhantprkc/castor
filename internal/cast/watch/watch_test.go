package watch

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stupside/castor/internal/media"
)

func TestTheWatchEndsOnAVerdictCarryingTheProducersError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sentinel := errors.New("upstream pull: exit status 183")
		producer := &fakeProducer{done: make(chan struct{}), err: sentinel, evidence: []string{"HTTP error 404 Not Found"}}
		close(producer.done)

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		err := Watch(ctx, Monitor{
			Subject: "playback gate", Window: BeforePlay,
			Producer: producer, Telemetry: producer,
			Landed: func() int64 { return 1 },
		})
		fault, ok := errors.AsType[*Fault](err)
		if !ok || fault.Kind != Dead || !fault.Revise || !errors.Is(err, sentinel) || len(fault.Evidence) != 1 {
			t.Fatalf("Watch = %v, want a revisable dead verdict carrying the reader's error and stderr", err)
		}
	})
}

// fakeProducer is a read that has already settled.
type fakeProducer struct {
	done     chan struct{}
	err      error
	evidence []string
}

func (f *fakeProducer) Progress() media.Progress { return media.Progress{} }
func (f *fakeProducer) Done() <-chan struct{}    { return f.done }
func (f *fakeProducer) Err() error               { return f.err }
func (f *fakeProducer) Evidence() []string       { return f.evidence }

func TestBytesWithNoMediaBehindThemAreAStall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A dead live edge: the muxer keeps writing tables while the media position never moves.
		producer := &frozenProducer{done: make(chan struct{}), position: 11 * time.Second}
		var landed int64
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
		defer cancel()
		err := Watch(ctx, Monitor{
			Subject: "the playing cast", Window: Playing,
			Producer: producer, Telemetry: producer,
			Landed: func() int64 { landed += 188; return landed },
		})
		if fault, ok := errors.AsType[*Fault](err); !ok || fault.Kind != Stalled {
			t.Fatalf("Watch = %v, want a stall once no media arrived for the stall window", err)
		}
	})
}

// frozenProducer is a read whose media position stopped while it keeps running.
type frozenProducer struct {
	done     chan struct{}
	position time.Duration
}

func (f *frozenProducer) Progress() media.Progress {
	return media.Progress{Position: f.position, Speed: 1}
}
func (f *frozenProducer) Done() <-chan struct{} { return f.done }
func (f *frozenProducer) Err() error            { return nil }
func (f *frozenProducer) Evidence() []string    { return nil }
