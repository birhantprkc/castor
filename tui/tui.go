// Package tui is castor's command line: what each command does over castor's public API, and how it shows it, as `castor cast` and `castor scan`.
package tui

import (
	"context"
	"errors"
	"log/slog"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/transport"
	"github.com/stupside/castor/tui/internal/browse/tmdb"
	"github.com/stupside/castor/tui/internal/cast"
	"github.com/stupside/castor/tui/internal/sources"
)

// Config is the command line's sections of castor's configuration.
type Config struct {
	Device  cast.Device   `yaml:"device" validate:"omitempty"`
	Sources sources.Sites `yaml:"sources" validate:"dive"`
	TMDB    tmdb.Config   `yaml:"tmdb"`
	API     apiConfig     `yaml:"api"`
}

// apiConfig is castor's API elsewhere, and the token it asks for; unset, the commands run one in their own process.
type apiConfig struct {
	URL   string `yaml:"url" validate:"omitempty,http_url"`
	Token string `yaml:"token"`
}

// Local runs castor's API in this process, its servers' own lines going to lines; written reports that a cast's lines already reach them.
type Local func(ctx context.Context, cmd *cli.Command, lines slog.Handler) (api transport.Endpoint, written bool, stop func(), err error)

// browsable refuses an interactive cast that has no catalog to browse.
func (c Config) browsable() error {
	if c.TMDB.APIKey == "" {
		return errors.New("TMDB API key missing: set tmdb.api_key in config.yaml or CASTOR_TMDB__API_KEY env var")
	}
	return nil
}
