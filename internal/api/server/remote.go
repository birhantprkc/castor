package server

import (
	"context"
	"net/url"
	"time"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/api/wire"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

// remoteRenderer is the lent device as the engine sees it: every call is made on the client that lent it.
type remoteRenderer struct{ s *session }

func (r remoteRenderer) Profile() media.Capabilities { return r.s.line.lent() }

func (r remoteRenderer) Connect(ctx context.Context) (device.Device, error) {
	cmd := &castorv1.DeviceCommand{Command: &castorv1.DeviceCommand_Connect_{Connect: &castorv1.DeviceCommand_Connect{}}}
	a, err := r.s.line.call(ctx, cmd)
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

// Play hands what this cast serves on as a path the client relays, and anything else (the source) as is.
func (d *remoteDevice) Play(ctx context.Context, streamURL *url.URL, contentType string) error {
	play := &castorv1.DeviceCommand_Play{Handle: d.handle, ContentType: contentType}
	if relayed, ok := d.s.relays.path(streamURL.Host); ok {
		relayed += streamURL.EscapedPath()
		if streamURL.RawQuery != "" {
			relayed += "?" + streamURL.RawQuery
		}
		play.Target = &castorv1.DeviceCommand_Play_RelayPath{RelayPath: relayed}
	} else {
		play.Target = &castorv1.DeviceCommand_Play_Url{Url: streamURL.String()}
	}
	return d.done(ctx, &castorv1.DeviceCommand{Command: &castorv1.DeviceCommand_Play_{Play: play}})
}

func (d *remoteDevice) AwaitEnd(ctx context.Context) error {
	return d.done(ctx, &castorv1.DeviceCommand{Command: &castorv1.DeviceCommand_AwaitEnd_{AwaitEnd: &castorv1.DeviceCommand_AwaitEnd{Handle: d.handle}}})
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
