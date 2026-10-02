// Package config is the composition root: the operator's settings, and every adapter and strategy bound to them.
package config

import (
	"context"
	"errors"
	"time"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/api/client"
	"github.com/stupside/castor/internal/api/server"
	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/compose"
	"github.com/stupside/castor/internal/cast/deliver"
	"github.com/stupside/castor/internal/cast/execute"
	"github.com/stupside/castor/internal/cast/transcode"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/device/chromecast"
	"github.com/stupside/castor/internal/device/dlna"
	"github.com/stupside/castor/internal/device/roku"
	"github.com/stupside/castor/internal/extract"
	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/probe"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/dash"
	"github.com/stupside/castor/internal/source/follow"
	"github.com/stupside/castor/internal/source/hls"
	"github.com/stupside/castor/internal/source/rank"
	"github.com/stupside/castor/internal/subtitle"
	"github.com/stupside/castor/internal/titles"
)

type Config struct {
	Device    DeviceConfig          `yaml:"device" validate:"omitempty"`
	Cast      CastConfig            `yaml:"cast"`
	Network   NetworkConfig         `yaml:"network" validate:"required"`
	Browser   extract.BrowserConfig `yaml:"browser" validate:"required"`
	Capture   extract.CaptureConfig `yaml:"capture" validate:"required"`
	Sources   titles.Sites          `yaml:"sources" validate:"dive"`
	Resolver  ResolverConfig        `yaml:"resolver" validate:"required"`
	Transcode TranscodeConfig       `yaml:"transcode" validate:"required"`
	Whisper   subtitle.Whisper      `yaml:"whisper"`
	TMDB      TMDB                  `yaml:"tmdb"`
	Remote    RemoteConfig          `yaml:"remote"`
	Server    ServerConfig          `yaml:"server" validate:"required"`

	// client is the one origin session of this process, so every cast and identification share its cookies.
	client func() source.Client
}

// TMDB holds settings for the TMDB browse subcommand.
type TMDB struct {
	APIKey string `yaml:"api_key"`
}

// RemoteConfig is the castor server on another machine this one's casts run on.
type RemoteConfig struct {
	URL string `yaml:"url" validate:"omitempty,http_url"`
}

// ServerConfig is this machine as `castor server`: where it listens, and where TVs reach it.
type ServerConfig struct {
	Listen string `yaml:"listen" validate:"required,hostname_port|startswith=:"`
	// Advertise is where TVs reach this server from outside its network.
	Advertise string `yaml:"advertise" validate:"omitempty,http_url"`
}

// Embedded reports whether casts run on a server inside castor itself.
func (c *Config) Embedded() bool { return c.Remote.URL == "" }

// TranscodeConfig is the transcode section: the ffmpeg binary, and how long one upstream read may stall.
type TranscodeConfig struct {
	FFmpegPath string        `yaml:"ffmpeg_path" validate:"required"`
	RWTimeout  time.Duration `yaml:"rw_timeout" validate:"required"`
}

// CastConfig is the cast-behaviour section: the decisions castor cannot infer.
type CastConfig struct {
	Delivery compose.DeliveryPreference `yaml:"delivery" validate:"omitempty,oneof=auto serve"`
}

// Devices is every device family castor casts to, bound to this config's family settings.
func (c *Config) Devices() device.Registry {
	return device.Registry{dlna.Family{}, chromecast.Family{}, roku.Family{Config: c.Device.Roku}}
}

// formats is every source format castor reads, in the order a source is offered to them.
var formats = source.Formats{hls.Format{}, dash.Format{}}

// DeviceConfig is the generic cast target plus each device family's optional connect settings.
type DeviceConfig struct {
	// Name identifies the device to discovery.
	Name string      `yaml:"name" validate:"required_without=Host"`
	Type device.Type `yaml:"type" validate:"required"`

	Host string `yaml:"host"`

	Roku roku.Config `yaml:"roku"`
}

// Target is the device this config names, as discovery reports devices; only a cast with no picker needs one.
func (c *Config) Target() (device.Info, error) {
	if c.Device.Type == "" {
		return device.Info{}, errors.New("no device to cast to: set device.name and device.type (castor scan lists them)")
	}
	return device.Info{Name: c.Device.Name, Type: c.Device.Type, Address: c.Device.Host}, nil
}

// execution is the machinery a cast asked as asked runs on renderer, serving on listeners; subs is the burn-in cmd binds, since the transcriber is cgo.
func (c *Config) execution(asked *castorv1.Preferences, renderer execute.Renderer, listeners deliver.Listeners, subs execute.Subtitles) execute.Config {
	return execute.Config{
		FFmpegPath: c.Transcode.FFmpegPath,
		Binary:     ffmpeg.Inspect(c.Transcode.FFmpegPath),
		Encoders:   transcode.Encoders(c.Transcode.FFmpegPath),
		Probes:     probe.FFprobe(c.Resolver.FFprobePath),
		Renderer:   renderer,
		Listeners:  listeners,
		Subtitles:  subs,
		MaxHeight:  media.HeightCap(asked.GetMaxHeight()),
		// Half the read deadline: a reload castor answers late would end ffmpeg's read like no answer.
		Timelines: follow.New(c.client(), formats, c.Transcode.RWTimeout/2, ffmpeg.Repackager(c.Transcode.FFmpegPath)),
	}
}

// resolver reads links for a cast asked as asked, on this process's one origin session.
func (c *Config) resolver(asked *castorv1.Preferences) *source.Resolver {
	return source.NewResolver(c.client(), media.HeightCap(asked.GetMaxHeight()), formats)
}

// Backend binds the API server to this config's machinery; burn is the burn-in cmd binds, since the transcriber is cgo.
func (c *Config) Backend(burn func(subtitle.Whisper) execute.Subtitles) server.Backend {
	return server.Backend{
		Extractor: c.extractor(),
		Caster: func(asked *castorv1.Preferences) server.Caster {
			// The model is this server's; whether to transcribe, and in what language, is the cast's.
			whisper := c.Whisper
			whisper.Language = asked.GetSubtitles()
			ranking := rank.Config{ProbeMaxConcurrency: c.Resolver.ProbeMaxConcurrency, MaxHeight: media.HeightCap(asked.GetMaxHeight())}
			return caster{
				Ranker: rank.New(ranking, probe.Stream(c.Resolver.FFprobePath, c.Resolver.ProbeTimeout)),
				config: c,
				asked:  asked,
				subs:   burn(whisper),
			}
		},
	}
}

// caster ranks and plays one cast as it was asked.
type caster struct {
	*rank.Ranker
	config *Config
	asked  *castorv1.Preferences
	subs   execute.Subtitles
}

// Measure names what a link carries when it says nothing, then measures it.
func (k caster) Measure(ctx context.Context, s *source.Stream) (*source.Stream, error) {
	if s.ContentType == "" {
		s.ContentType = k.config.resolver(k.asked).Identify(ctx, s.URL)
	}
	return k.Ranker.Measure(ctx, s)
}

func (k caster) Play(ctx context.Context, renderer execute.Renderer, listeners deliver.Listeners, streams []*source.Stream, turns attempt.Turns) error {
	// The ranked streams, head first; the rest are what recovery switches to.
	return attempt.Cast(ctx, attempt.Intent{
		Candidates: streams,
		Deadline:   k.config.Transcode.RWTimeout,
		Delivery:   delivery(k.asked.GetDelivery()),
		Turns:      turns,
	}, execute.NewExecutor(k.config.execution(k.asked, renderer, listeners, k.subs)), k.config.resolver(k.asked))
}

// Preferences is what this machine's operator asks of every cast it starts.
func (c *Config) Preferences() *castorv1.Preferences {
	asked := &castorv1.Preferences{Delivery: castorv1.Delivery_DELIVERY_AUTO, MaxHeight: int32(c.Resolver.MaxHeight)}
	if c.Cast.Delivery == compose.DeliveryServe {
		asked.Delivery = castorv1.Delivery_DELIVERY_SERVE
	}
	if c.Whisper.Enable {
		asked.Subtitles = c.Whisper.Language
	}
	return asked
}

// delivery is the engine's reading of the delivery a cast asked for.
func delivery(d castorv1.Delivery) compose.DeliveryPreference {
	if d == castorv1.Delivery_DELIVERY_SERVE {
		return compose.DeliveryServe
	}
	return compose.DeliveryAuto
}

// Renderers binds a client to the renderers on this machine's network.
func (c *Config) Renderers() client.Renderers {
	return renderers{families: c.Devices(), timeout: c.Network.Timeout}
}

type renderers struct {
	families device.Registry
	timeout  time.Duration
}

func (r renderers) SelfFetches(t device.Type) bool { return r.families.Profile(t).SelfFetch }

func (r renderers) Connect(ctx context.Context, target device.Info) (device.Device, error) {
	return r.families.Connect(ctx, target, r.timeout)
}

// NetworkConfig: how long discovery and a device protocol are given, and the interface renderers reach castor on.
type NetworkConfig struct {
	Timeout   time.Duration `yaml:"timeout" validate:"required"`
	Interface string        `yaml:"interface"`
}

// ResolverConfig is the resolver section: what source reads, plus the adapters this root binds for it.
type ResolverConfig struct {
	rank.Config `yaml:",inline"`

	// PlaylistTimeout bounds each HLS or DASH document fetch.
	PlaylistTimeout time.Duration `yaml:"playlist_timeout" validate:"required"`
	FFprobePath     string        `yaml:"ffprobe_path" validate:"required"`
	ProbeTimeout    time.Duration `yaml:"probe_timeout" validate:"required"`
}

// extractor finds the streams a page plays, recognising documents in every format castor reads.
func (c *Config) extractor() *extract.Extractor {
	return extract.New(extract.Config{
		Browser:   c.Browser,
		Capture:   c.Capture,
		Documents: formats,
	})
}
