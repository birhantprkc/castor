package server

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/media"
)

func connectCommand() *castorv1.DeviceCommand {
	return &castorv1.DeviceCommand{Command: &castorv1.DeviceCommand_Connect_{Connect: &castorv1.DeviceCommand_Connect{}}}
}

// attached is a line whose driver's commands arrive on the returned channel.
func attached(t *testing.T) (*line, <-chan *castorv1.DeviceCommand) {
	t.Helper()
	l := newLine()
	sent := make(chan *castorv1.DeviceCommand, 8)
	if err := l.attach(media.Capabilities{}, func(cmd *castorv1.DeviceCommand) error {
		sent <- cmd
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return l, sent
}

func TestACastTakesOneDevice(t *testing.T) {
	l, _ := attached(t)
	err := l.attach(media.Capabilities{}, func(*castorv1.DeviceCommand) error { return nil })
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a second device was met with %v, want it refused", err)
	}
}

func TestACallIsAnsweredByItsDevice(t *testing.T) {
	l, sent := attached(t)
	answered := make(chan error, 1)
	go func() {
		_, err := l.call(t.Context(), connectCommand())
		answered <- err
	}()
	cmd := <-sent
	if !l.answer(&castorv1.AnswerRequest{CommandId: cmd.GetId()}) {
		t.Fatal("the answer found no call awaiting it")
	}
	if err := <-answered; err != nil {
		t.Errorf("the call ended with %v, want its answer", err)
	}
	if l.answer(&castorv1.AnswerRequest{CommandId: cmd.GetId()}) {
		t.Error("a call was answered twice")
	}
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
		ctx, abandon := context.WithCancel(t.Context())
		go func() { _, _ = l.call(ctx, connectCommand()) }()
		cmd := <-sent
		abandon()
		if cancel := (<-sent).GetCancel(); cancel.GetCommandId() != cmd.GetId() {
			t.Errorf("the device was sent %v, want the call cancelled", cancel)
		}
		if l.answer(&castorv1.AnswerRequest{CommandId: cmd.GetId()}) {
			t.Error("an abandoned call still awaited its answer")
		}
	})
}

func TestACastNobodyLendsADeviceIsAbandonedAfterItsGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const grace = time.Minute
		c := &casts{ctx: t.Context(), caster: func(*castorv1.Preferences) Caster { return nil }, undriven: grace, registry: newRegistry()}
		started, err := c.StartCast(t.Context(), &castorv1.StartCastRequest{})
		if err != nil {
			t.Fatal(err)
		}
		s, err := c.registry.find(started.GetCastId())
		if err != nil {
			t.Fatal(err)
		}

		time.Sleep(grace - time.Nanosecond)
		synctest.Wait()
		if _, _, over, _ := s.status.read(); over {
			t.Fatal("abandoned before its grace ran out")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if _, _, _, outcome := s.status.read(); !errors.Is(outcome, errUndriven) {
			t.Errorf("ended with %v, want the undriven cause", outcome)
		}
	})
}
