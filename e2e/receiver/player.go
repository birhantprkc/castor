package receiver

import (
	"context"
	"io"

	"github.com/stupside/castor/e2e/strategy"
)

// Player consumes a handed stream onto a tape, the way one kind of media is played.
type Player interface {
	strategy.Named
	// Plays reports whether this player takes a response whose body opens with head.
	Plays(head []byte) bool
	Record(ctx context.Context, rec Recording) (tape string, err error)
}

// Recording is one handed stream a Player consumes.
type Recording struct {
	URL string
	// Body is the response already opened on URL.
	Body   io.Reader
	Tape   string
	FFmpeg string
	Viewer Viewer
}
