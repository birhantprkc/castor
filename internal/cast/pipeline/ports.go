package pipeline

import (
	"context"
	"io"
	"net/url"

	"golang.org/x/sync/errgroup"

	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// Target is the configured renderer before there is one. It answers what the composition
// table needs to know without a network round trip, and then produces the connected
// renderer.
//
// Answering the static fact through this port is what keeps a device family out of the cast
// layer: the self-fetch bit used to be a package-level question about a device TYPE, asked
// beside the capability record that already carries the same field, held together by a
// comment saying the two must agree, and forked on at the top of the executor.
type Target interface {
	// Profile is what is knowable about this renderer before discovery. Every field it does
	// not answer is zero, and zero means unmeasured, so no rule that needs negotiated
	// capabilities can accidentally read it (see the composition table's two passes).
	Profile() media.Renderer

	// Acquire discovers and connects the renderer. The caller owns closing it.
	Acquire(ctx context.Context) (Renderer, error)
}

// Renderer is a connected renderer as a composition drives it. It is declared here, at the
// consumer, rather than taken as the device package's own interface: a fake needs no device
// family, and the cast layer names none.
type Renderer interface {
	// Capabilities is what this renderer negotiated: the containers it takes as they are,
	// the envelopes it decodes, and the container it asks to be served.
	Capabilities() media.Renderer

	// Play points the renderer at a URL, advertised as contentType. Only the composition
	// that hands over the source URL calls this itself; the served ones leave it to the
	// delivery driver.
	Play(ctx context.Context, streamURL *url.URL, contentType string) error

	// StreamHeaders are the protocol headers a response fronting a served stream must carry
	// for this renderer.
	StreamHeaders(contentType string) map[string]string

	Close() error
}

// Stage is an optional per-cast stage the composer starts, feeds and reads inputs from. It
// is a port so that "this cast burns subtitles" stops being a nil pointer re-tested at every
// site that touches it, and so the caption sidecar the subtitle axis already anticipates is
// another implementation rather than another nil-shaped axis.
type Stage interface {
	// Attach binds the stage to one cast's outputs and runs it in g until the feed ends. A
	// stage that fails must keep draining what it was given: backpressure on the audio feed
	// throttles the read the whole cast is fed by.
	Attach(ctx context.Context, g *errgroup.Group, pcm io.ReadCloser)

	// Inputs is what the encode must carry for this stage to work: the live text file to
	// draw, empty for a stage that needs nothing drawn. The file exists by the time this
	// returns, which is what keeps "a burn-in forces a re-encode" a property of the video
	// decision rather than an ordering contract between two statements at a call site.
	Inputs() (burnIn string, err error)

	// Follow is the step this stage takes per sample the encoder reports about itself. It is
	// a step rather than a loop because the delivery driver drains that feed whether any
	// stage exists or not (an unread -progress pipe stops the encoder dead), so there is
	// exactly one reader of it and every consumer is a callback.
	Follow(ctx context.Context) func(media.Progress)

	// Lead is how far this stage has committed, for the readiness rule that waits on it, or
	// nil for a stage nothing has to wait for.
	Lead() watch.Lead
}
