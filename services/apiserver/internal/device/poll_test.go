package device

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// A poll that hangs for its whole timeout must not stretch the window the device is judged gone in.
func TestADeviceIsGoneAfterTheWindowHoweverLongEachPollHangs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		hangs := func(ctx context.Context) (bool, error) {
			<-ctx.Done()
			return false, errors.New("i/o timeout")
		}
		err := AwaitPolledEnd(t.Context(), "Living Room", "GetTransportInfo", hangs)
		if _, gone := errors.AsType[*Gone](err); !gone {
			t.Fatalf("AwaitPolledEnd = %v, want the device gone", err)
		}
		if took := time.Since(start); took > UnreachableWindow+PollTimeout+PollInterval {
			t.Errorf("judged gone after %s, want within one poll of the %s window", took, UnreachableWindow)
		}
	})
}
