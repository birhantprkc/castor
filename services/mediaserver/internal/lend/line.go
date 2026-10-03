// Package lend is a cast's hold on the device its lender connected: the line commands travel down, and the device the engine plays on.
package lend

import (
	"context"
	"errors"
	"strconv"
	"sync"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
)

// ErrLenderLeft is every call on a line whose lender has left.
var ErrLenderLeft = errors.New("the API server that lent the device left")

// Line is a cast's one link to its device: the calls Drive sends down its stream, and those awaiting answers.
type Line struct {
	left chan struct{}
	// outbox hands each command to the Drive handler, the only goroutine that sends on its stream.
	outbox chan *mediav1.DeviceCommand

	mu       sync.Mutex
	next     int
	awaiting map[string]chan *mediav1.AnswerRequest
}

func NewLine() *Line {
	return &Line{
		left:     make(chan struct{}),
		outbox:   make(chan *mediav1.DeviceCommand),
		awaiting: map[string]chan *mediav1.AnswerRequest{},
	}
}

// Outbox is each command to send down the lender's stream.
func (l *Line) Outbox() <-chan *mediav1.DeviceCommand { return l.outbox }

// Leave cuts the line: every call on it fails from now on.
func (l *Line) Leave() { close(l.left) }

// Call has the device run cmd and waits for its answer; abandoning the wait cancels it there too.
func (l *Line) Call(ctx context.Context, cmd *mediav1.DeviceCommand) (*mediav1.AnswerRequest, error) {
	answered := make(chan *mediav1.AnswerRequest, 1)
	l.mu.Lock()
	l.next++
	id := strconv.Itoa(l.next)
	l.awaiting[id] = answered
	l.mu.Unlock()
	cmd.Id = id
	defer l.forget(id)

	if err := l.send(ctx, cmd); err != nil {
		return nil, err
	}
	select {
	case a := <-answered:
		return a, nil
	case <-l.left:
		return nil, ErrLenderLeft
	case <-ctx.Done():
		// The cancel waits for the stream, never the caller that gave up.
		go l.send(context.Background(), &mediav1.DeviceCommand{Command: &mediav1.DeviceCommand_Cancel_{Cancel: &mediav1.DeviceCommand_Cancel{CommandId: id}}})
		return nil, ctx.Err()
	}
}

func (l *Line) send(ctx context.Context, cmd *mediav1.DeviceCommand) error {
	select {
	case l.outbox <- cmd:
		return nil
	case <-l.left:
		return ErrLenderLeft
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Answer hands a to the call awaiting it, reporting whether one was.
func (l *Line) Answer(a *mediav1.AnswerRequest) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	answered, ok := l.awaiting[a.GetCommandId()]
	if ok {
		answered <- a
		delete(l.awaiting, a.GetCommandId())
	}
	return ok
}

func (l *Line) forget(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.awaiting, id)
}
