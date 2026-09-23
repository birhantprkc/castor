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
