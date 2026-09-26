// Package viewer is how the person in front of a receiver watches.
package viewer

import (
	"context"
	"io"
)

// ToTheEnd watches everything, reading as fast as the stream comes.
type ToTheEnd struct{}

func (ToTheEnd) Name() string { return "to-the-end" }

func (ToTheEnd) Watch(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(ctx)
}
func (ToTheEnd) Stopped(context.Context) bool { return false }
func (ToTheEnd) WillStop() bool               { return false }
func (ToTheEnd) Pace(r io.Reader) io.Reader   { return r }
func (ToTheEnd) Realtime() bool               { return false }
