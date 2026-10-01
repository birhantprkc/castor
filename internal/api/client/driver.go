package client

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/api/wire"
	"github.com/stupside/castor/internal/device"
)

// Drive lends target to cast id, running the server's calls on it until the cast ends or ctx does.
func (c *Client) Drive(ctx context.Context, id string, target device.Info) error {
	stream, err := c.devices.Drive(ctx, &castorv1.DriveRequest{
		CastId:  id,
		Device:  &castorv1.Device{Name: target.Name, Type: string(target.Type), Address: target.Address},
		Profile: wire.Capabilities(c.lan.Renderers.Profile(target.Type)),
	})
	if err != nil {
		return fmt.Errorf("driving cast: %w", err)
	}
	defer func() { _ = stream.Close() }()
	d := newDriver(ctx, c, id, target)
	defer d.release()
	for stream.Receive() {
		d.run(stream.Msg().GetCommand())
	}
	if err := stream.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("driving cast: %w", err)
	}
	return nil
}

// driver runs the server's calls on the client's renderer, each concurrently, for one drive.
type driver struct {
	ctx    context.Context
	c      *Client
	castID string
	lent   device.Info

	relay func() (*relay, error)

	mu      sync.Mutex
	devices map[string]device.Device
	running map[string]context.CancelFunc
	wg      sync.WaitGroup
}

func newDriver(ctx context.Context, c *Client, castID string, target device.Info) *driver {
	d := &driver{ctx: ctx, c: c, castID: castID, lent: target, devices: map[string]device.Device{}, running: map[string]context.CancelFunc{}}
	d.relay = sync.OnceValues(func() (*relay, error) { return openRelay(ctx, c.base, c.lan.Address) })
	return d
}

func (d *driver) run(cmd *castorv1.DeviceCommand) {
	if cancel := cmd.GetCancel(); cancel != nil {
		d.mu.Lock()
		if stop, ok := d.running[cancel.GetCommandId()]; ok {
			stop()
		}
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(d.ctx)
	d.mu.Lock()
	d.running[cmd.GetId()] = cancel
	d.mu.Unlock()
	d.wg.Go(func() {
		defer func() {
			d.mu.Lock()
			delete(d.running, cmd.GetId())
			d.mu.Unlock()
			cancel()
		}()
		answer := d.exec(ctx, cmd)
		// A cancelled call is abandoned on the server, which awaits no answer.
		if ctx.Err() != nil {
			return
		}
		answer.CastId, answer.CommandId = d.castID, cmd.GetId()
		d.c.answer(ctx, answer)
	})
}

func (d *driver) exec(ctx context.Context, cmd *castorv1.DeviceCommand) *castorv1.AnswerRequest {
	switch c := cmd.GetCommand().(type) {
	case *castorv1.DeviceCommand_Connect_:
		dev, err := d.c.lan.Renderers.Connect(ctx, d.lent)
		if err != nil {
			return failure(err)
		}
		d.mu.Lock()
		d.devices[cmd.GetId()] = dev
		d.mu.Unlock()
		return &castorv1.AnswerRequest{Answer: &castorv1.AnswerRequest_Capabilities{Capabilities: wire.Capabilities(dev.Capabilities())}}
	case *castorv1.DeviceCommand_Play_:
		dev, err := d.device(c.Play.GetHandle())
		if err != nil {
			return failure(err)
		}
		target, err := d.target(c.Play)
		if err != nil {
			return failure(err)
		}
		return outcome(dev.Play(ctx, target, c.Play.GetContentType()))
	case *castorv1.DeviceCommand_StreamHeaders_:
		dev, err := d.device(c.StreamHeaders.GetHandle())
		if err != nil {
			return failure(err)
		}
		headers := dev.StreamHeaders(c.StreamHeaders.GetContentType())
		return &castorv1.AnswerRequest{Answer: &castorv1.AnswerRequest_Headers_{Headers: &castorv1.AnswerRequest_Headers{Headers: headers}}}
	case *castorv1.DeviceCommand_AwaitEnd_:
		dev, err := d.device(c.AwaitEnd.GetHandle())
		if err != nil {
			return failure(err)
		}
		return outcome(dev.AwaitEnd(ctx))
	case *castorv1.DeviceCommand_Close_:
		d.mu.Lock()
		dev, ok := d.devices[c.Close.GetHandle()]
		delete(d.devices, c.Close.GetHandle())
		d.mu.Unlock()
		if !ok {
			return outcome(nil)
		}
		return outcome(dev.Close())
	}
	return failure(fmt.Errorf("device command %q is one this client does not know", cmd.GetId()))
}

func (d *driver) device(handle string) (device.Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	dev, ok := d.devices[handle]
	if !ok {
		return nil, fmt.Errorf("no renderer is connected under %q", handle)
	}
	return dev, nil
}

// target is the source as is, or this client's relay of what the server serves.
func (d *driver) target(play *castorv1.DeviceCommand_Play) (*url.URL, error) {
	if path := play.GetRelayPath(); path != "" {
		r, err := d.relay()
		if err != nil {
			return nil, err
		}
		return r.url(path)
	}
	return url.Parse(play.GetUrl())
}

// release abandons every call still running and closes every renderer still open: nothing drives them past this drive.
func (d *driver) release() {
	d.mu.Lock()
	for _, stop := range d.running {
		stop()
	}
	d.mu.Unlock()
	d.wg.Wait()
	d.mu.Lock()
	for handle, dev := range d.devices {
		_ = dev.Close()
		delete(d.devices, handle)
	}
	d.mu.Unlock()
	if r, err := d.relay(); err == nil {
		r.close()
	}
}

func outcome(err error) *castorv1.AnswerRequest {
	if err != nil {
		return failure(err)
	}
	return &castorv1.AnswerRequest{Answer: &castorv1.AnswerRequest_Done_{Done: &castorv1.AnswerRequest_Done{}}}
}

func failure(err error) *castorv1.AnswerRequest {
	return &castorv1.AnswerRequest{Answer: &castorv1.AnswerRequest_Error{Error: wire.DeviceError(err)}}
}

func (c *Client) answer(ctx context.Context, req *castorv1.AnswerRequest) {
	answer, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlTimeout)
	defer cancel()
	if _, err := c.devices.Answer(answer, req); err != nil {
		slog.DebugContext(ctx, "answering device command", "command", req.GetCommandId(), "error", err)
	}
}
