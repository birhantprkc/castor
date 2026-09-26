// Package config is the composition root: the operator's settings, and every adapter and strategy bound to them.
package config

import (
	"context"
	"net/url"
	"time"

	"github.com/stupside/castor/internal/cast"
	"github.com/stupside/castor/internal/cast/compose"
	"github.com/stupside/castor/internal/cast/execute"
	"github.com/stupside/castor/internal/cast/netaddr"
	"github.com/stupside/castor/internal/cast/transcode"
	"github.com/stupside/castor/internal/catalog"
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
)

type Config struct {
	Device    DeviceConfig          `yaml:"device" validate:"required"`
	Cast      CastConfig            `yaml:"cast"`
	Network   NetworkConfig         `yaml:"network" validate:"required"`
	Browser   extract.BrowserConfig `yaml:"browser" validate:"required"`
	Capture   extract.CaptureConfig `yaml:"capture" validate:"required"`
	Sources   catalog.Sites         `yaml:"sources" validate:"dive"`
	Resolver  ResolverConfig        `yaml:"resolver" validate:"required"`
	Transcode TranscodeConfig       `yaml:"transcode" validate:"required"`
	Whisper   subtitle.Whisper      `yaml:"whisper"`
	TMDB      TMDB                  `yaml:"tmdb"`

	// client is THE origin session for this process, memoised (see defaults), so resolution and following share its cookies.
	client func() source.Client
	source func() *source.Resolver
	ranker func() *rank.Ranker
}

// TMDB holds settings for the TMDB browse subcommand.
type TMDB struct {
	APIKey string `yaml:"api_key"`
}

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

// Formats is every source format castor reads, in the order a source is offered to them.
var Formats = source.Formats{hls.Format{}, dash.Format{}}

// DeviceConfig is the generic cast target plus each device family's optional connect settings.
type DeviceConfig struct {
	// Name identifies the device to discovery.
	Name string      `yaml:"name" validate:"required_without=Host"`
	Type device.Type `yaml:"type" validate:"required"`

	Host string `yaml:"host"`

	Roku roku.Config `yaml:"roku"`
}

// Target is the device this config names, as discovery reports devices.
func (c *Config) Target() device.Info {
	return device.Info{Name: c.Device.Name, Type: c.Device.Type, Address: c.Device.Host}
}

// Playback binds a cast to target; subs is the burn-in cmd binds, since the transcriber is cgo.
func (c *Config) Playback(target device.Info, subs execute.Subtitles) cast.Config {
	return cast.Config{
		Source:       c.source(),
		Delivery:     c.Cast.Delivery,
		ReadDeadline: c.Transcode.RWTimeout,
		Execute: execute.Config{
			FFmpegPath: c.Transcode.FFmpegPath,
			Binary:     ffmpeg.Inspect(c.Transcode.FFmpegPath),
			Encoders:   transcode.Encoders(c.Transcode.FFmpegPath),
			Probes:     probe.FFprobe(c.Resolver.FFprobePath),
			Renderer:   configured{families: c.Devices(), target: target, timeout: c.Network.Timeout},
			Addresses:  netaddr.Local{Interface: c.Network.Interface},
			Subtitles:  subs,
			MaxHeight:  c.Resolver.MaxHeight,
			// Half the read deadline: a reload castor answers late would end ffmpeg's read like no answer.
			Timelines: follow.New(c.client(), Formats, c.Transcode.RWTimeout/2, ffmpeg.Repackager(c.Transcode.FFmpegPath)),
		},
	}
}

type configured struct {
	families device.Registry
	target   device.Info
	timeout  time.Duration
}

func (c configured) Profile() media.Capabilities { return c.families.Profile(c.target.Type) }

func (c configured) Connect(ctx context.Context) (device.Device, error) {
	return c.families.Connect(ctx, c.target, c.timeout)
}

// NetworkConfig: how long discovery and a device protocol are given, and which interface a local relay binds.
type NetworkConfig struct {
	Timeout   time.Duration `yaml:"timeout" validate:"required"`
	Interface string        `yaml:"interface"`
}

// Ranker is the one ranker this process uses, built on first call and shared by every caller after.
func (c *Config) Ranker() *rank.Ranker { return c.ranker() }

// ResolverConfig is the resolver section: what source reads, plus the adapters this root binds for it.
type ResolverConfig struct {
	rank.Config `yaml:",inline"`

	// PlaylistTimeout bounds each HLS or DASH document fetch.
	PlaylistTimeout time.Duration `yaml:"playlist_timeout" validate:"required"`
	FFprobePath     string        `yaml:"ffprobe_path" validate:"required"`
	ProbeTimeout    time.Duration `yaml:"probe_timeout" validate:"required"`
}

func (c *Config) newResolver() *source.Resolver {
	return source.NewResolver(c.client(), c.Resolver.MaxHeight, Formats)
}

// Identify names what a typed link carries, reading its body when its name says nothing.
func (c *Config) Identify(ctx context.Context, u *url.URL) string {
	return c.source().Identify(ctx, u)
}

// newRanker binds ranking to the same measurement resolution identifies a source with.
func (c *Config) newRanker() *rank.Ranker {
	return rank.New(c.Resolver.Config, probe.Candidate(c.Resolver.FFprobePath, c.Resolver.ProbeTimeout))
}

// Extractor finds candidate streams on a page, recognising documents in every format castor reads.
func (c *Config) Extractor() *extract.Extractor {
	return extract.New(extract.Config{
		Browser:   c.Browser,
		Capture:   c.Capture,
		Documents: Formats,
	})
}
