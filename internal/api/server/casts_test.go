package server

import (
	"errors"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
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
		if _, _, over, _ := s.status.read(); over {
			t.Fatal("abandoned before its grace ran out")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if _, _, _, outcome := s.status.read(); !errors.Is(outcome, errUndriven) {
			t.Errorf("ended with %v, want the undriven cause", outcome)
		}
	})
}
