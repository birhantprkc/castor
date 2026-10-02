package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"

	"connectrpc.com/connect"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/api/wire"
	"github.com/stupside/castor/internal/device"
)

// Drive lends target to cast id, running the server's calls on it until the cast ends or ctx does.
func (c *Client) Drive(parent context.Context, id string, target device.Info) error {
	// Leaving ends the stream and every call on the renderer; the cast plays on without it.
	ctx, leave := context.WithCancelCause(parent)
	defer leave(nil)
	stream, err := c.devices.Drive(ctx, &castorv1.DriveRequest{
		CastId:    id,
		Device:    &castorv1.Device{Name: target.Name, Type: string(target.Type), Address: target.Address},
		SelfFetch: c.renderers.SelfFetches(target.Type),
	})
	if err != nil {
		return fmt.Errorf("driving cast: %w", refused(err))
	}
	defer func() { _ = stream.Close() }()
	d := &driver{ctx: ctx, leave: leave, c: c, castID: id, lent: target, running: map[string]context.CancelFunc{}}
	defer d.release()
	for stream.Receive() {
		d.run(stream.Msg().GetCommand())
	}
	if cause := context.Cause(ctx); cause != nil && parent.Err() == nil {
		return cause
	}
	if err := stream.Err(); err != nil && parent.Err() == nil {
		return fmt.Errorf("driving cast: %w", refused(err))
	}
	return nil
}

// driver runs the server's calls on the client's renderer, each concurrently, for one drive.
type driver struct {
	ctx    context.Context
	leave  context.CancelCauseFunc
	c      *Client
	castID string
	lent   device.Info

	mu      sync.Mutex
	dev     device.Device
	running map[string]context.CancelFunc
	wg      sync.WaitGroup
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
		d.answer(ctx, answer)
	})
}

func (d *driver) exec(ctx context.Context, cmd *castorv1.DeviceCommand) *castorv1.AnswerRequest {
	switch cmd.GetCommand().(type) {
	case *castorv1.DeviceCommand_Connect_:
		d.disconnect()
		dev, err := d.c.renderers.Connect(ctx, d.lent)
		if err != nil {
			return failure(err)
		}
		d.mu.Lock()
		d.dev = dev
		d.mu.Unlock()
		return &castorv1.AnswerRequest{Answer: &castorv1.AnswerRequest_Capabilities{Capabilities: wire.Capabilities(dev.Capabilities())}}
	case *castorv1.DeviceCommand_Close_:
		return outcome(d.disconnect())
	}
	dev, err := d.connected()
	if err != nil {
		return failure(err)
	}
	switch c := cmd.GetCommand().(type) {
	case *castorv1.DeviceCommand_Play_:
		target, err := url.Parse(c.Play.GetUrl())
		if err != nil {
			return failure(err)
		}
		return outcome(dev.Play(ctx, target, c.Play.GetContentType()))
	case *castorv1.DeviceCommand_StreamHeaders_:
		headers := dev.StreamHeaders(c.StreamHeaders.GetContentType())
		return &castorv1.AnswerRequest{Answer: &castorv1.AnswerRequest_Headers_{Headers: &castorv1.AnswerRequest_Headers{Headers: headers}}}
	case *castorv1.DeviceCommand_AwaitEnd_:
		return outcome(dev.AwaitEnd(ctx))
	}
	return failure(fmt.Errorf("device command %q is one this client does not know", cmd.GetId()))
}

func (d *driver) connected() (device.Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dev == nil {
		return nil, errors.New("no renderer is connected")
	}
	return d.dev, nil
}

// disconnect closes the renderer this drive has open, if any.
func (d *driver) disconnect() error {
	d.mu.Lock()
	dev := d.dev
	d.dev = nil
	d.mu.Unlock()
	if dev == nil {
		return nil
	}
	return dev.Close()
}

// release abandons every call still running and closes the renderer: nothing drives it past this drive.
func (d *driver) release() {
	d.mu.Lock()
	for _, stop := range d.running {
		stop()
	}
	d.mu.Unlock()
	d.wg.Wait()
	_ = d.disconnect()
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

// answer replies to the server; an answer lost on the way would leave the cast waiting on it, so the drive ends instead.
func (d *driver) answer(ctx context.Context, req *castorv1.AnswerRequest) {
	answering, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlTimeout)
	defer cancel()
	// NotFound is a call the server already gave up on; it awaits nothing.
	if _, err := d.c.devices.Answer(answering, req); err != nil && connect.CodeOf(err) != connect.CodeNotFound {
		d.leave(fmt.Errorf("answering device command %s: %w", req.GetCommandId(), err))
	}
}
