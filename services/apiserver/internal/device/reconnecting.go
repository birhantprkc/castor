package device

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
)

// reconnecting is the device a cast lends, so later attempts still reach it after its connection is gone.
type reconnecting struct {
	dir *Directory

	mu     sync.Mutex
	target Info
	device Device
}

func (r *reconnecting) current() (Device, Info) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.device, r.target
}

func (r *reconnecting) Play(ctx context.Context, streamURL *url.URL, container mediav1.Container) error {
	dev, target := r.current()
	err := dev.Play(ctx, streamURL, container)
	if _, gone := errors.AsType[*Gone](err); !gone {
		return err
	}
	fresh, at, cerr := r.dir.dial(ctx, target)
	if cerr != nil {
		return fmt.Errorf("%w; connecting again: %w", err, cerr)
	}
	r.mu.Lock()
	stale := r.device
	r.device, r.target = fresh, at
	r.mu.Unlock()
	_ = stale.Close()
	return fresh.Play(ctx, streamURL, container)
}

func (r *reconnecting) AwaitEnd(ctx context.Context) error {
	dev, _ := r.current()
	return dev.AwaitEnd(ctx)
}

func (r *reconnecting) Capabilities() *mediav1.Capabilities {
	dev, _ := r.current()
	return dev.Capabilities()
}

func (r *reconnecting) Close() error {
	dev, _ := r.current()
	return dev.Close()
}
