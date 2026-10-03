package lend

import (
	"context"
	"errors"
	"net/url"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
	"github.com/stupside/castor/services/mediaserver/internal/cast/execute"
	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/wire"
)

// Device is the lent device as the engine sees it: its lender holds it for the whole cast and plays what it is asked.
type Device struct {
	line *Line
	caps media.Capabilities
	// reach is where the device fetches a URL the engine serves on loopback.
	reach func(*url.URL) *url.URL
	// left ends the cast once its lender has gone.
	left func()
}

var _ execute.Device = Device{}

func NewDevice(line *Line, caps media.Capabilities, reach func(*url.URL) *url.URL, left func()) Device {
	return Device{line: line, caps: caps, reach: reach, left: left}
}

func (d Device) Play(ctx context.Context, streamURL *url.URL, contentType string) error {
	play := &mediav1.DeviceCommand_Play{Url: d.reach(streamURL).String(), Container: wire.Container(contentType)}
	err := d.done(ctx, &mediav1.DeviceCommand{Command: &mediav1.DeviceCommand_Play_{Play: play}})
	if errors.Is(err, ErrLenderLeft) {
		// No attempt reaches a device whose lender has left, so recovery tries none.
		d.left()
	}
	return err
}

// AwaitEnd waits for the device's end; once its lender has left, the cast's deliveries decide it.
func (d Device) AwaitEnd(ctx context.Context) error {
	err := d.done(ctx, &mediav1.DeviceCommand{Command: &mediav1.DeviceCommand_AwaitEnd_{AwaitEnd: &mediav1.DeviceCommand_AwaitEnd{}}})
	if errors.Is(err, ErrLenderLeft) {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (d Device) Capabilities() media.Capabilities { return d.caps }

func (d Device) done(ctx context.Context, cmd *mediav1.DeviceCommand) error {
	a, err := d.line.Call(ctx, cmd)
	if err != nil {
		return err
	}
	if e := a.GetError(); e != nil {
		return wire.FromDeviceError(e)
	}
	return nil
}
