// Package probe measures links and local files with ffprobe.
package probe

import (
	"context"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// FFprobe is the ffprobe binary a cast measures its source and its local buffer with.
type FFprobe string

// File binds a local path to this ffprobe; safe to point at a still-growing spool.
func (bin FFprobe) File(path string) media.Prober {
	return fileProber{ffprobePath: string(bin), path: path}
}

type fileProber struct{ ffprobePath, path string }

func (p fileProber) Probe(ctx context.Context) (media.ProbeInfo, media.Reach, error) {
	return pass{ffprobePath: p.ffprobePath, input: p.path}.run(ctx)
}
