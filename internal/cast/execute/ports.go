package execute

import (
	"context"
	"io"
	"net/url"

	"github.com/stupside/castor/internal/cast/deliver"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

type Renderer interface {
	// Profile is what is knowable about this renderer before discovery; every unset field means unmeasured.
	Profile() media.Capabilities

	// Connect discovers and connects the media; the caller owns closing it.
	Connect(ctx context.Context) (device.Device, error)
}

type Subtitles func(ctx context.Context, workDir string) Burn

type Burn interface {
	// Lead is how far the transcription has committed, which the playback gate waits on.
	watch.Lead

	// Run transcribes the PCM feed until it ends, and closes it.
	Run(ctx context.Context, pcm io.ReadCloser)

	// SampleRate is the rate of the mono PCM feed Run reads.
	SampleRate() int

	Inputs() (burnIn string, err error)

	Follow(ctx context.Context) func(media.Progress)
}

// Probes measures what an attempt reads: its source program, or its local buffer.
type Probes interface {
	Source(program media.Program, inputs []ffmpeg.ProbeInput) media.Prober
	File(path string) media.Prober
}

type Addresses interface {
	// LocalIPv4 performs a syscall Termux denies, so stays a port.
	LocalIPv4(ctx context.Context) (string, error)
}

type sink interface {
	URL() *url.URL

	Wait(ctx context.Context) error

	Artifact() deliver.Artifact

	Drained() <-chan struct{}

	Audience() watch.Audience

	Settled() error

	Close() error
}

// Timelines republishes a program's followed inputs for one read; the func stops serving them.
type Timelines interface {
	Republish(ctx context.Context, program media.Program) (media.Program, func() error, error)
}
