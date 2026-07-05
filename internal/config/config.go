// Package config assembles the application configuration. It sits at the
// edge of the dependency graph: every section's type is owned by the package
// that consumes it (cast, extractor, resolve, whisper) and composed here, so
// domain packages never import application-level state.
package config

import (
	"strconv"
	"strings"

	"github.com/stupside/castor/internal/cast"
	"github.com/stupside/castor/internal/cast/whisper"
	"github.com/stupside/castor/internal/source/extract"
	"github.com/stupside/castor/internal/source/resolve"
)

type Config struct {
	Device    cast.DeviceConfig     `yaml:"device" validate:"required"`
	Network   cast.NetworkConfig    `yaml:"network" validate:"required"`
	Browser   extract.BrowserConfig `yaml:"browser" validate:"required"`
	Capture   extract.CaptureConfig `yaml:"capture" validate:"required"`
	Actions   extract.ActionConfig  `yaml:"actions" validate:"required"`
	Sources   []Source              `yaml:"sources" validate:"dive"`
	Resolver  resolve.Config        `yaml:"resolver" validate:"required"`
	Transcode cast.TranscodeConfig  `yaml:"transcode" validate:"required"`
	Whisper   whisper.Config        `yaml:"whisper"`
	TMDB      TMDB                  `yaml:"tmdb"`
}

// TMDB holds settings for the TMDB browse subcommand. The API key may also
// be supplied via the CASTOR_TMDB__API_KEY environment variable, so it is
// intentionally not marked required here.
type TMDB struct {
	APIKey string `yaml:"api_key"`
}

func (c *Config) Cast() cast.Config {
	return cast.Config{
		Device:    c.Device,
		Network:   c.Network,
		Transcode: c.Transcode,
		Whisper:   c.Whisper,
		Resolver:  c.Resolver,
	}
}

func (c *Config) Extractor() extract.Config {
	return extract.Config{
		Browser: c.Browser,
		Capture: c.Capture,
		Actions: c.Actions,
	}
}

// Source defines a set of proxy hosts and the URL templates to reach a movie
// or episode page on them.
type Source struct {
	Proxies   []string  `yaml:"proxies" validate:"required,min=1"`
	Templates Templates `yaml:"templates" validate:"required"`
}

func (c *Config) AllMovieURLs(itemID string) []string {
	var urls []string
	for _, s := range c.Sources {
		urls = append(urls, s.MovieURLs(itemID)...)
	}
	return urls
}

func (c *Config) AllEpisodeURLs(itemID string, season, episode uint) []string {
	var urls []string
	for _, s := range c.Sources {
		urls = append(urls, s.EpisodeURLs(itemID, season, episode)...)
	}
	return urls
}

type Templates struct {
	Movie   string `yaml:"movie" validate:"required"`
	Episode string `yaml:"episode" validate:"required"`
}

func (s *Source) MovieURLs(itemID string) []string {
	return s.expandTemplate(s.Templates.Movie, "{itemID}", itemID)
}

func (s *Source) EpisodeURLs(itemID string, season, episode uint) []string {
	return s.expandTemplate(s.Templates.Episode,
		"{itemID}", itemID,
		"{season}", strconv.FormatUint(uint64(season), 10),
		"{episode}", strconv.FormatUint(uint64(episode), 10),
	)
}

// expandTemplate substitutes placeholder/value pairs into tmpl and prefixes
// the result with every proxy host.
func (s *Source) expandTemplate(tmpl string, pairs ...string) []string {
	route := strings.NewReplacer(pairs...).Replace(tmpl)
	urls := make([]string, len(s.Proxies))
	for i, proxy := range s.Proxies {
		urls[i] = proxy + route
	}
	return urls
}
