package cast

import (
	"context"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/services/mediaserver/internal/cast/deliver"
	"github.com/stupside/castor/services/mediaserver/internal/cast/execute"
	"github.com/stupside/castor/services/mediaserver/internal/cast/recovery"
	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source"
)

func TestACastNobodyLendsADeviceIsAbandonedAfterItsGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := New(nil, func(*castorv1.Preferences) Caster { return nil }, &url.URL{Scheme: "http", Host: "127.0.0.1:8410"})
		started, err := svc.Start(t.Context(), &mediav1.StartRequest{})
		if err != nil {
			t.Fatal(err)
		}
		s, err := svc.find(started.GetCastId())
		if err != nil {
			t.Fatal(err)
		}

		synctest.Sleep(undriven - time.Nanosecond)
		if now, _ := s.now.Load(); now.ended != nil {
			t.Fatal("abandoned before its grace ran out")
		}
		synctest.Sleep(time.Nanosecond)
		if now, _ := s.now.Load(); now.ended.GetOutcome() != castorv1.Outcome_OUTCOME_FAILED || now.ended.GetReason() != errUndriven.Error() {
			t.Errorf("ended %v, want failed for want of a device", now.ended)
		}
	})
}

// retrying tries twice, revises once, then plays until the cast is stopped.
type retrying struct{}

func (retrying) Rank(_ context.Context, streams []*source.Stream) ([]*source.Stream, error) {
	return streams, nil
}

func (retrying) Measure(_ context.Context, stream *source.Stream) (*source.Stream, error) {
	return stream, nil
}

func (retrying) Play(ctx context.Context, _ execute.Device, _ deliver.Listeners, _ []*source.Stream, turns recovery.Turns) error {
	turns.Attempting(1)
	turns.Attempting(2)
	turns.Revising("remux", "stalled")
	<-ctx.Done()
	return ctx.Err()
}

func TestALentCastOutlivesItsGraceAndShowsNothingAfterItsEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stream := &castorv1.Source{Source: &castorv1.Source_Stream{Stream: &castorv1.Stream{Url: "https://cdn.example/direct"}}}
		s := newCast(t.Context(), "cast", &url.URL{Scheme: "http", Host: "127.0.0.1:8410"}, nil, retrying{}, stream)
		if err := s.lend(media.Capabilities{}); err != nil {
			t.Fatal(err)
		}

		synctest.Sleep(undriven)
		now, _ := s.now.Load()
		if now.ended != nil {
			t.Fatal("a lent cast was abandoned when its grace ran out")
		}
		if now.status.GetAttempt() != 2 || now.status.GetRevision().GetStrategy() != "remux" {
			t.Errorf("shown %v, want the second attempt and its revision", now.status)
		}

		s.cancel(errStopped)
		<-s.done
		s.Revising("encode", "too late")
		if now, _ := s.now.Load(); now.ended.GetOutcome() != castorv1.Outcome_OUTCOME_STOPPED || now.status.GetRevision().GetStrategy() != "remux" {
			t.Errorf("after its end the cast shows %+v, want its outcome and nothing more", now)
		}
		if connect.CodeOf(s.lend(media.Capabilities{})) != connect.CodeFailedPrecondition {
			t.Error("a cast that is over took a device")
		}
	})
}
