package lend

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
)

func awaitEndCommand() *mediav1.DeviceCommand {
	return &mediav1.DeviceCommand{Command: &mediav1.DeviceCommand_AwaitEnd_{AwaitEnd: &mediav1.DeviceCommand_AwaitEnd{}}}
}

// attached is a line whose commands arrive on the returned channel, as Drive would take them, until it is cut.
func attached(t *testing.T) (*Line, <-chan *mediav1.DeviceCommand) {
	t.Helper()
	l := NewLine()
	sent := make(chan *mediav1.DeviceCommand, 8)
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
			_, err := l.Call(t.Context(), awaitEndCommand())
			answered <- err
		}()
		<-sent
		l.Leave()
		if err := <-answered; !errors.Is(err, ErrLenderLeft) {
			t.Errorf("a call awaiting a device that left ended with %v", err)
		}
		if _, err := l.Call(t.Context(), awaitEndCommand()); !errors.Is(err, ErrLenderLeft) {
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
		defer l.Leave()
		ctx, abandon := context.WithCancel(t.Context())
		go func() { _, _ = l.Call(ctx, awaitEndCommand()) }()
		cmd := <-sent
		abandon()
		if cancel := (<-sent).GetCancel(); cancel.GetCommandId() != cmd.GetId() {
			t.Errorf("the device was sent %v, want the call cancelled", cancel)
		}
		synctest.Wait()
		if l.Answer(&mediav1.AnswerRequest{CommandId: cmd.GetId()}) {
			t.Error("an abandoned call still awaited its answer")
		}
	})
}
