package device

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stupside/castor/internal/media"
)

// A set that sleeps makes each poll hang for its whole timeout, which must not stretch the window it is judged gone in.
func TestARendererIsGoneAfterTheWindowHoweverLongEachPollHangs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		hangs := func(ctx context.Context) (bool, error) {
			<-time.After(PollTimeout)
			return false, errors.New("i/o timeout")
		}
		err := AwaitPolledEnd(t.Context(), "Living Room TV", "GetTransportInfo", UnreachableWindow, PollInterval, hangs)
		if _, gone := errors.AsType[*media.Gone](err); !gone {
			t.Fatalf("AwaitPolledEnd = %v, want the renderer gone", err)
		}
		if took := time.Since(start); took > UnreachableWindow+PollTimeout+PollInterval {
			t.Errorf("judged gone after %s, want within one poll of the %s window", took, UnreachableWindow)
		}
	})
}
