package server

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/deliver"
	"github.com/stupside/castor/internal/cast/execute"
	"github.com/stupside/castor/internal/source"
)

func TestACastNobodyLendsADeviceIsAbandonedAfterItsGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &casts{ctx: t.Context(), caster: func(*castorv1.Preferences) Caster { return nil }, server: &url.URL{Scheme: "http", Host: "127.0.0.1:8410"}, registry: newRegistry()}
		started, err := c.Start(t.Context(), &castorv1.StartRequest{})
		if err != nil {
			t.Fatal(err)
		}
		s, err := c.registry.find(started.GetCastId())
		if err != nil {
			t.Fatal(err)
		}

		time.Sleep(undriven - time.Nanosecond)
		synctest.Wait()
		if s.now.Load().over {
			t.Fatal("abandoned before its grace ran out")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if outcome := s.now.Load().outcome; !errors.Is(outcome, errUndriven) {
			t.Errorf("ended with %v, want the undriven cause", outcome)
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

func (retrying) Play(ctx context.Context, _ execute.Renderer, _ deliver.Listeners, _ []*source.Stream, turns attempt.Turns) error {
	turns.Attempting(1)
	turns.Attempting(2)
	turns.Revising("remux", "stalled")
	<-ctx.Done()
	return ctx.Err()
}

func TestALentCastOutlivesItsGraceAndShowsNothingAfterItsEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		s := newSession(ctx, "cast", cancel, &url.URL{Scheme: "http", Host: "127.0.0.1:8410"}, retrying{}, &castorv1.StartRequest{})
		if err := s.lend(false); err != nil {
			t.Fatal(err)
		}

		time.Sleep(undriven)
		synctest.Wait()
		now := s.now.Load()
		if now.over {
			t.Fatal("a lent cast was abandoned when its grace ran out")
		}
		if now.status.GetAttempt() != 2 || now.status.GetRevision().GetStrategy() != "remux" {
			t.Errorf("shown %v, want the second attempt and its revision", now.status)
		}

		cancel(errStopped)
		<-s.done
		s.Revising("encode", "too late")
		if now := s.now.Load(); !errors.Is(now.outcome, errStopped) || now.status.GetRevision().GetStrategy() != "remux" {
			t.Errorf("after its end the cast shows %+v, want its outcome and nothing more", now)
		}
		if connect.CodeOf(s.lend(false)) != connect.CodeFailedPrecondition {
			t.Error("a cast that is over took a device")
		}
	})
}
