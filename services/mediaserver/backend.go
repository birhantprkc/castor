package mediaserver

import (
	"context"
	"log/slog"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/services/mediaserver/internal/cast"
	"github.com/stupside/castor/services/mediaserver/internal/cast/attempt"
	"github.com/stupside/castor/services/mediaserver/internal/cast/deliver"
	"github.com/stupside/castor/services/mediaserver/internal/cast/execute"
	"github.com/stupside/castor/services/mediaserver/internal/extract"
	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/probe"
	"github.com/stupside/castor/services/mediaserver/internal/source"
	"github.com/stupside/castor/services/mediaserver/internal/source/dash"
	"github.com/stupside/castor/services/mediaserver/internal/source/follow"
	"github.com/stupside/castor/services/mediaserver/internal/source/hls"
	"github.com/stupside/castor/services/mediaserver/internal/source/rank"
	"github.com/stupside/castor/services/mediaserver/internal/source/web"
	"github.com/stupside/castor/services/mediaserver/internal/subtitle/whisper"
	"github.com/stupside/castor/services/mediaserver/internal/wire"
)

// formats is every source format castor reads, in the order a source is offered to them.
var formats = source.Formats{hls.Format{}, dash.Format{}}

// backend is the machinery the media server casts with, bound to c.
func (c *Config) backend() Backend {
	// One origin session for the engine, so every cast and identification share its cookies.
	client := web.New(c.Resolver.PlaylistTimeout)
	e := &engine{
		cfg:    *c,
		client: client,
		machinery: execute.Machinery{
			FFmpegPath: c.Transcode.FFmpegPath,
			Binary:     ffmpeg.Inspect(c.Transcode.FFmpegPath),
			Encoders:   ffmpeg.Encoders(c.Transcode.FFmpegPath),
			Probes:     probe.FFprobe(c.Resolver.FFprobePath),
			// Half the read deadline: a reload castor answers late would end ffmpeg's read like no answer.
			Timelines: follow.New(client, formats, c.Transcode.RWTimeout/2, follow.Repackager(c.Transcode.FFmpegPath)),
			InputArgs: formats.InputArgs,
		},
	}
	return Backend{
		Extractor: extract.New(extract.Config{Browser: c.Browser, Capture: c.Capture, Documents: formats}),
		Caster: func(asked *castorv1.Preferences) cast.Caster {
			return caster{
				Ranker: rank.New(c.Resolver.Config, media.HeightCap(asked.GetMaxHeight()), func(s *source.Stream) media.Prober {
					return e.machinery.Probes.Link(probe.Link{URL: s.URL, Headers: s.Headers, InputArgs: formats.InputArgs(s.ContentType, 0)}, c.Resolver.ProbeTimeout)
				}),
				engine: e,
				asked:  asked,
				subs:   e.subtitles(asked.GetSubtitles()),
			}
		},
	}
}

type engine struct {
	cfg       Config
	client    source.Client
	machinery execute.Machinery
}

// subtitles burns in language with the configured whisper model, none when language is empty.
func (e *engine) subtitles(language string) execute.Subtitles {
	if language == "" {
		return nil
	}
	return func(ctx context.Context, workDir string) execute.Burn {
		b, err := whisper.New(ctx, e.cfg.Whisper, language, workDir)
		if err != nil {
			slog.WarnContext(ctx, "whisper init failed; casting without subtitles", "error", err)
			return nil
		}
		return b
	}
}

// resolver reads links for a cast asked as asked.
func (e *engine) resolver(asked *castorv1.Preferences) *source.Resolver {
	return source.NewResolver(e.client, media.HeightCap(asked.GetMaxHeight()), formats)
}

// caster ranks and plays one cast as it was asked.
type caster struct {
	*rank.Ranker
	engine *engine
	asked  *castorv1.Preferences
	subs   execute.Subtitles
}

// Measure names what a link carries when it says nothing, then measures it.
func (k caster) Measure(ctx context.Context, s *source.Stream) (*source.Stream, error) {
	if s.ContentType == "" {
		s.ContentType = formats.Identify(ctx, k.engine.client, s.URL)
	}
	return k.Ranker.Measure(ctx, s)
}

func (k caster) Play(ctx context.Context, device execute.Device, listeners deliver.Listeners, streams []*source.Stream, turns attempt.Turns) error {
	run := execute.NewExecutor(k.engine.machinery, execute.Cast{
		Device:    device,
		Listeners: listeners,
		Subtitles: k.subs,
		MaxHeight: media.HeightCap(k.asked.GetMaxHeight()),
	})
	// The ranked streams, head first; the rest are what recovery switches to.
	return attempt.Cast(ctx, attempt.Intent{
		Candidates: streams,
		Deadline:   k.engine.cfg.Transcode.RWTimeout,
		Delivery:   wire.FromDelivery(k.asked.GetDelivery()),
		Turns:      turns,
	}, run, k.engine.resolver(k.asked))
}
