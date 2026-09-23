package config

import (
	"context"
	"time"

	"github.com/stupside/castor/internal/browse/tmdb"
	"github.com/stupside/castor/internal/cast"
	"github.com/stupside/castor/internal/cast/engine/ffmpeg"
	"github.com/stupside/castor/internal/cast/execute"
	"github.com/stupside/castor/internal/cast/policy/compose"
	"github.com/stupside/castor/internal/catalog"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/device/chromecast"
	"github.com/stupside/castor/internal/device/dlna"
	"github.com/stupside/castor/internal/device/roku"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/netaddr"
	"github.com/stupside/castor/internal/probe"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/dash"
	"github.com/stupside/castor/internal/source/extract"
	"github.com/stupside/castor/internal/source/hls"
	"github.com/stupside/castor/internal/source/web"
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

	// source is THE source resolver for this process, memoised (see defaults), and ranker THE ranker.
	source func() *source.Resolver
	ranker func() *source.Ranker
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
			Encoders:   ffmpeg.Encoders(c.Transcode.FFmpegPath),
			Probes:     probe.FFprobe(c.Resolver.FFprobePath),
			Renderer:   configured{families: c.Devices(), target: target, timeout: c.Network.Timeout},
			Addresses:  netaddr.Local{Interface: c.Network.Interface},
			Subtitles:  subs,
			MaxHeight:  c.Resolver.MaxHeight,
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
func (c *Config) Ranker() *source.Ranker { return c.ranker() }

// ResolverConfig is the resolver section: what source reads, plus the adapters this root binds for it.
type ResolverConfig struct {
	source.Config `yaml:",inline"`

	// PlaylistTimeout bounds each HLS or DASH document fetch.
	PlaylistTimeout time.Duration `yaml:"playlist_timeout" validate:"required"`
	FFprobePath     string        `yaml:"ffprobe_path" validate:"required"`
	ProbeTimeout    time.Duration `yaml:"probe_timeout" validate:"required"`
}

func (c *Config) newResolver() *source.Resolver {
	return source.NewResolver(c.Resolver.Config, web.Playlists(c.Resolver.PlaylistTimeout), Formats)
}

// newRanker binds ranking to the same measurement resolution identifies a source with.
func (c *Config) newRanker() *source.Ranker {
	return source.NewRanker(c.Resolver.Config, c.probes())
}

func (c *Config) probes() source.Probes {
	return probe.Candidate(c.Resolver.FFprobePath, c.Resolver.ProbeTimeout)
}

// Extractor finds candidate streams on a page, recognising documents in every format castor reads.
func (c *Config) Extractor() *extract.Extractor {
	return extract.New(extract.Config{
		Browser:   c.Browser,
		Capture:   c.Capture,
		Documents: Formats,
	})
}

// Catalog is the TMDB client the interactive browser searches.
func (c *Config) Catalog() *tmdb.Client { return tmdb.New(c.TMDB.APIKey) }
