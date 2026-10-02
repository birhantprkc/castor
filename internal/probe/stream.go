package probe

import (
	"context"
	"log/slog"
	"time"

	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/rank"
)

// Stream measures a found stream within timeout, opening it as castor would read it.
func (bin FFprobe) Stream(timeout time.Duration) rank.Probes {
	return func(s *source.Stream) media.Prober {
		return streamProber{ffprobePath: string(bin), timeout: timeout, stream: s}
	}
}

type streamProber struct {
	ffprobePath string
	timeout     time.Duration
	stream      *source.Stream
}

func (p streamProber) Probe(ctx context.Context) (media.ProbeInfo, media.Reach, error) {
	s := p.stream
	args := ffmpeg.HeaderArgs(s.Headers)
	if adaptive := ffmpeg.AdaptiveInputArgs(s.ContentType, 0); adaptive != nil {
		args = append(args, adaptive...)
	} else {
		args = append(args, ffmpeg.LenientInputArgs()...)
	}

	slog.DebugContext(ctx, "running ffprobe", "url", s.URL.String(), "header_count", len(s.Headers))

	info, reach, err := pass{
		ffprobePath: p.ffprobePath,
		budget:      p.timeout,
		inputArgs:   args,
		input:       s.URL.String(),
	}.run(ctx)
	if err != nil {
		return media.ProbeInfo{}, reach, err
	}
	if info.ContentType == "" {
		slog.DebugContext(ctx, "unrecognised container; the source will be read rather than handed over",
			"url", s.URL.String())
	}
	return info, reach, nil
}
