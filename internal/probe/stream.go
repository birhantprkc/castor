package probe

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/rank"
)

func Stream(ffprobePath string, timeout time.Duration) rank.Probes {
	return func(s *source.Stream) media.Prober {
		return streamProber{ffprobePath: ffprobePath, timeout: timeout, stream: s}
	}
}

type streamProber struct {
	ffprobePath string
	timeout     time.Duration
	stream      *source.Stream
}

func (p streamProber) Probe(ctx context.Context) (media.ProbeInfo, media.Reach, error) {
	s := p.stream
	if s == nil || s.URL == nil {
		return media.ProbeInfo{}, media.ReachUnproven, fmt.Errorf("probing source: no URL")
	}

	args := ffmpeg.HeaderArgs(s.Headers)
	args = append(args, probeInputArgs(s.ContentType)...)

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

func probeInputArgs(contentType string) []string {
	if args := ffmpeg.AdaptiveInputArgs(contentType, 0); args != nil {
		return args
	}
	return ffmpeg.LenientInputArgs()
}
