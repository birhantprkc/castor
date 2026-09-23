// Package devicetest: every device family answers to this test suite.
package devicetest

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
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

// DeclaresTheUniversalBaseline: H.264 High 8-bit is universal (re-encode else).
func DeclaresTheUniversalBaseline(t *testing.T, caps media.Capabilities) {
	t.Helper()
	baseline := media.ProbeInfo{VideoCodec: media.CodecH264, VideoProfile: media.ProfileHigh, VideoBitDepth: 8}
	if !caps.CanCopyVideo(baseline) {
		t.Error("the family refuses 8-bit High-profile H.264, the universal baseline")
	}
}

// DeclaresNoModelSpecificCodec: family name >= models; no runtime model-specific guesses.
func DeclaresNoModelSpecificCodec(t *testing.T, caps media.Capabilities) {
	t.Helper()
	if caps.SupportsCodec(media.CodecHEVC) {
		t.Error("the family declares model-specific HEVC support without runtime evidence")
	}
}
