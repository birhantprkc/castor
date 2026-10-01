package server

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"connectrpc.com/connect"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/media"
)

// line is a cast's one link to its device: the calls Drive sends down its stream, and those awaiting answers.
type line struct {
	attached chan struct{}
	left     chan struct{}
	// outbox hands each command to the Drive handler, the only goroutine that sends on its stream.
	outbox chan *castorv1.DeviceCommand

	mu       sync.Mutex
	profile  media.Capabilities
	next     int
	awaiting map[string]chan *castorv1.AnswerRequest
}

func newLine() *line {
	return &line{
		attached: make(chan struct{}),
		left:     make(chan struct{}),
		outbox:   make(chan *castorv1.DeviceCommand),
		awaiting: map[string]chan *castorv1.AnswerRequest{},
	}
}

// attach lends the line the device profile describes; a cast takes one device, so a second is refused.
func (l *line) attach(profile media.Capabilities) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.attached:
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this cast already has its device"))
	default:
	}
	l.profile = profile
	close(l.attached)
	return nil
}

// leave cuts the line: every call on it fails from now on.
func (l *line) leave() { close(l.left) }

func (l *line) lent() media.Capabilities {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.profile
}

// call has the device run cmd and waits for its answer; abandoning the wait cancels it there too.
func (l *line) call(ctx context.Context, cmd *castorv1.DeviceCommand) (*castorv1.AnswerRequest, error) {
	answered := make(chan *castorv1.AnswerRequest, 1)
	l.mu.Lock()
	l.next++
	cmd.Id = strconv.Itoa(l.next)
	l.awaiting[cmd.Id] = answered
	l.mu.Unlock()
	defer l.forget(cmd.Id)

	if err := l.send(ctx, cmd); err != nil {
		return nil, err
	}
	select {
	case a := <-answered:
		return a, nil
	case <-l.left:
		return nil, errDriverLeft
	case <-ctx.Done():
		// The cancel waits for the stream, never the caller: it goes once Drive takes it, or never once the line is cut.
		go func() {
			_ = l.send(context.Background(), &castorv1.DeviceCommand{Command: &castorv1.DeviceCommand_Cancel_{Cancel: &castorv1.DeviceCommand_Cancel{CommandId: cmd.Id}}})
		}()
		return nil, ctx.Err()
	}
}

func (l *line) send(ctx context.Context, cmd *castorv1.DeviceCommand) error {
	select {
	case l.outbox <- cmd:
		return nil
	case <-l.left:
		return errDriverLeft
	case <-ctx.Done():
		return ctx.Err()
	}
}

// answer hands a to the call awaiting it, reporting whether one was.
func (l *line) answer(a *castorv1.AnswerRequest) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	answered, ok := l.awaiting[a.GetCommandId()]
	if ok {
		answered <- a
		delete(l.awaiting, a.GetCommandId())
	}
	return ok
}

func (l *line) forget(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.awaiting, id)
}
