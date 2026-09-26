package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/stupside/castor/internal/extract"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/rank"
	"github.com/stupside/castor/internal/source/web"
	"github.com/stupside/castor/internal/subtitle"
)

// Defaults is the base configuration layer, as the real typed Config so a mistyped key cannot compile.
func Defaults() *Config {
	cfg := &Config{
		Network: NetworkConfig{Timeout: 5 * time.Second},
		Browser: extract.BrowserConfig{Timeout: 30 * time.Second, Headless: true},
		Resolver: ResolverConfig{
			Config:          rank.Config{ProbeMaxConcurrency: 2, MaxHeight: 1080},
			PlaylistTimeout: 30 * time.Second,
			FFprobePath:     "ffprobe",
			ProbeTimeout:    30 * time.Second,
		},
		Capture:   extract.CaptureConfig{ParallelURLs: 4},
		Transcode: TranscodeConfig{FFmpegPath: "ffmpeg", RWTimeout: 30 * time.Second},
		Whisper:   subtitle.Whisper{Language: "en"},
	}
	cfg.client = sync.OnceValue(func() source.Client { return web.Client(cfg.Resolver.PlaylistTimeout) })
	cfg.source = sync.OnceValue(cfg.newResolver)
	cfg.ranker = sync.OnceValue(cfg.newRanker)
	return cfg
}

const envPrefix = "CASTOR_"

func Load(path string) (*Config, error) {
	k := koanf.New(".")

	if _, err := os.Stat(path); err == nil {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("loading %s: %w", path, err)
		}
	}
	local := strings.TrimSuffix(path, filepath.Ext(path)) + ".local" + filepath.Ext(path)
	if _, err := os.Stat(local); err == nil {
		if err := k.Load(file.Provider(local), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("loading %s: %w", local, err)
		}
	}
	if err := k.Load(env.Provider(envPrefix, ".", envKey), nil); err != nil {
		return nil, fmt.Errorf("loading environment overrides: %w", err)
	}

	cfg := Defaults()
	if err := k.UnmarshalWithConf("", cfg, koanf.UnmarshalConf{
		Tag: "yaml",
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
				mapstructure.TextUnmarshallerHookFunc(),
			),
			WeaklyTypedInput: true,
			SquashTagOption:  "inline",
		},
	}); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	if err := validator.New().Struct(cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}
	if err := cfg.Devices().Known(cfg.Device.Type); err != nil {
		return nil, fmt.Errorf("validating config: device.type: %w", err)
	}
	return cfg, nil
}

// envKey maps CASTOR_SECTION__FIELD to the koanf key section.field.
func envKey(s string) string {
	s = strings.TrimPrefix(s, envPrefix)
	s = strings.ToLower(s)
	return strings.ReplaceAll(s, "__", ".")
}
