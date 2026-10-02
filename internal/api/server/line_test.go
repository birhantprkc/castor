package server

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

func connectCommand() *castorv1.DeviceCommand {
	return &castorv1.DeviceCommand{Command: &castorv1.DeviceCommand_Connect_{Connect: &castorv1.DeviceCommand_Connect{}}}
}

// attached is a line whose commands arrive on the returned channel, as Drive would take them, until it is cut.
func attached(t *testing.T) (*line, <-chan *castorv1.DeviceCommand) {
	t.Helper()
	l := newLine()
	sent := make(chan *castorv1.DeviceCommand, 8)
	go func() {
		for {
			select {
			case cmd := <-l.outbox:
				sent <- cmd
			case <-l.left:
				return
			}
		}
	}()
	return l, sent
}

func TestACallFailsOnceItsDeviceLeft(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, sent := attached(t)
		answered := make(chan error, 1)
		go func() {
			_, err := l.call(t.Context(), connectCommand())
			answered <- err
		}()
		<-sent
		l.leave()
		if err := <-answered; !errors.Is(err, errDriverLeft) {
			t.Errorf("a call awaiting a device that left ended with %v", err)
		}
		if _, err := l.call(t.Context(), connectCommand()); !errors.Is(err, errDriverLeft) {
			t.Errorf("a call after the device left ended with %v", err)
		}
		if len(sent) != 0 {
			t.Error("a command was sent after the device left")
		}
	})
}

func TestAnAbandonedCallIsCancelledOnTheDevice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, sent := attached(t)
		defer l.leave()
		ctx, abandon := context.WithCancel(t.Context())
		go func() { _, _ = l.call(ctx, connectCommand()) }()
		cmd := <-sent
		abandon()
		if cancel := (<-sent).GetCancel(); cancel.GetCommandId() != cmd.GetId() {
			t.Errorf("the device was sent %v, want the call cancelled", cancel)
		}
		synctest.Wait()
		if l.answer(&castorv1.AnswerRequest{CommandId: cmd.GetId()}) {
			t.Error("an abandoned call still awaited its answer")
		}
	})
}
