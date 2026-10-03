package execute

import (
	"context"
	"io"
	"net/url"

	"github.com/stupside/castor/services/mediaserver/internal/cast/health"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// Device is what a cast plays on, connected for the whole cast by whoever lent it, who also closes it.
type Device interface {
	// Play points the device at streamURL, advertised as contentType.
	Play(ctx context.Context, streamURL *url.URL, contentType string) error

	AwaitEnd(ctx context.Context) error

	Capabilities() media.Capabilities
}

type Subtitles func(ctx context.Context, workDir string) Burn

type Burn interface {
	// Lead is how far the transcription has committed, which the playback gate waits on.
	health.Lead

	// SampleRate is the rate of the mono PCM feed Run reads.
	SampleRate() int

	// Run transcribes the PCM feed until it ends, and closes it.
	Run(ctx context.Context, pcm io.ReadCloser)

	Inputs() (burnIn string, err error)

	Follow(ctx context.Context) func(media.Progress)
}

// Timelines republishes a program's followed inputs for one read; the func stops serving them.
type Timelines interface {
	Republish(ctx context.Context, program media.Program) (media.Program, func() error, error)
}
