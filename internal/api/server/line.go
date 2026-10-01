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

// line is a cast's one link to its device: the Drive stream its calls go down, and the calls awaiting answers.
type line struct {
	attached chan struct{}
	left     chan struct{}

	mu       sync.Mutex
	driven   bool
	profile  media.Capabilities
	next     int
	awaiting map[string]chan *castorv1.AnswerRequest

	// writing serialises sends, and keeps any from landing once the stream has left.
	writing sync.Mutex
	send    func(*castorv1.DeviceCommand) error
}

func newLine() *line {
	return &line{attached: make(chan struct{}), left: make(chan struct{}), awaiting: map[string]chan *castorv1.AnswerRequest{}}
}

// attach makes send the line to the device profile describes; a cast takes one device, so a second is refused.
func (l *line) attach(profile media.Capabilities, send func(*castorv1.DeviceCommand) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.driven {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this cast already has its device"))
	}
	l.driven, l.profile, l.send = true, profile, send
	close(l.attached)
	return nil
}

// leave cuts the line: every call on it fails from now on, and nothing more is sent.
func (l *line) leave() {
	l.writing.Lock()
	defer l.writing.Unlock()
	close(l.left)
}

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

	if err := l.write(cmd); err != nil {
		return nil, err
	}
	select {
	case a := <-answered:
		return a, nil
	case <-l.left:
		return nil, errDriverLeft
	case <-ctx.Done():
		_ = l.write(&castorv1.DeviceCommand{Command: &castorv1.DeviceCommand_Cancel_{Cancel: &castorv1.DeviceCommand_Cancel{CommandId: cmd.Id}}})
		return nil, ctx.Err()
	}
}

func (l *line) write(cmd *castorv1.DeviceCommand) error {
	l.writing.Lock()
	defer l.writing.Unlock()
	select {
	case <-l.left:
		return errDriverLeft
	default:
	}
	return l.send(cmd)
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
