// Package devicetest: every device family answers to this test suite.
package devicetest

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stupside/castor/internal/device"
)

// pollsBeforeTeardown is how many poll intervals a running cast is watched before it is torn down.
const pollsBeforeTeardown = 5

// AwaitsTheCastsEnd: AwaitEnd outlasts polling until the cast ends; open runs in a synctest bubble, off the network.
func AwaitsTheCastsEnd(t *testing.T, open func() device.Device) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		dev := open()
		torn := errors.New("the cast was torn down")
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)

		ended := make(chan error, 1)
		go func() { ended <- dev.AwaitEnd(ctx) }()

		time.Sleep(pollsBeforeTeardown * device.PollInterval)
		synctest.Wait()
		select {
		case err := <-ended:
			t.Fatalf("AwaitEnd = %v while the cast was still running, so this family ends every delivery it is handed", err)
		default:
		}

		cancel(torn)
		select {
		case err := <-ended:
			if !errors.Is(err, torn) {
				t.Errorf("AwaitEnd = %v, want the reason the cast ended", err)
			}
		case <-time.After(device.PollTimeout):
			t.Fatal("AwaitEnd did not return when the cast ended, so every teardown waits on this family forever")
		}
	})
}
