package server

import (
	"context"
	"errors"
	"net/url"
	"time"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/api/wire"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

// remoteRenderer is the lent device as the engine sees it: every call is made on the client that lent it.
type remoteRenderer struct{ s *session }

func (r remoteRenderer) Profile() media.Capabilities {
	return media.Capabilities{SelfFetch: r.s.line.fetchesItself()}
}

func (r remoteRenderer) Connect(ctx context.Context) (device.Device, error) {
	cmd := &castorv1.DeviceCommand{Command: &castorv1.DeviceCommand_Connect_{Connect: &castorv1.DeviceCommand_Connect{}}}
	a, err := r.s.line.call(ctx, cmd)
	if errors.Is(err, errDriverLeft) {
		// No attempt reaches a device whose client has left, so recovery tries none.
		r.s.cancel(errDriverLeft)
	}
	if err != nil {
		return nil, err
	}
	if err := answerErr(a); err != nil {
		return nil, err
	}
	return &remoteDevice{s: r.s, handle: cmd.GetId(), caps: wire.FromCapabilities(a.GetCapabilities())}, nil
}

type remoteDevice struct {
	s      *session
	handle string
	caps   media.Capabilities
}

func (d *remoteDevice) Play(ctx context.Context, streamURL *url.URL, contentType string) error {
	play := &castorv1.DeviceCommand_Play{Handle: d.handle, Url: d.s.deliveries.reached(streamURL).String(), ContentType: contentType}
	return d.done(ctx, &castorv1.DeviceCommand{Command: &castorv1.DeviceCommand_Play_{Play: play}})
}

// AwaitEnd waits for the renderer's end; once its client has left, the cast's deliveries decide it.
func (d *remoteDevice) AwaitEnd(ctx context.Context) error {
	err := d.done(ctx, &castorv1.DeviceCommand{Command: &castorv1.DeviceCommand_AwaitEnd_{AwaitEnd: &castorv1.DeviceCommand_AwaitEnd{Handle: d.handle}}})
	if errors.Is(err, errDriverLeft) {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (d *remoteDevice) Capabilities() media.Capabilities { return d.caps }

// StreamHeaders asks under the cast's own context, the port having none of its own.
func (d *remoteDevice) StreamHeaders(contentType string) map[string]string {
	a, err := d.s.line.call(d.s.ctx, &castorv1.DeviceCommand{Command: &castorv1.DeviceCommand_StreamHeaders_{StreamHeaders: &castorv1.DeviceCommand_StreamHeaders{Handle: d.handle, ContentType: contentType}}})
	if err != nil || answerErr(a) != nil {
		return nil
	}
	return a.GetHeaders().GetHeaders()
}

// Close is asked under the cast's context with the teardown bound the engine gives everything else.
func (d *remoteDevice) Close() error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(d.s.ctx), closeTimeout)
	defer cancel()
	return d.done(ctx, &castorv1.DeviceCommand{Command: &castorv1.DeviceCommand_Close_{Close: &castorv1.DeviceCommand_Close{Handle: d.handle}}})
}

func (d *remoteDevice) done(ctx context.Context, cmd *castorv1.DeviceCommand) error {
	a, err := d.s.line.call(ctx, cmd)
	if err != nil {
		return err
	}
	return answerErr(a)
}

func answerErr(a *castorv1.AnswerRequest) error {
	if e := a.GetError(); e != nil {
		return wire.FromDeviceError(e)
	}
	return nil
}

// closeTimeout bounds a renderer's close, which runs as the attempt is torn down.
const closeTimeout = 5 * time.Second
