package main

import (
	"errors"

	"github.com/stupside/castor/cmd/castor/internal/browse/tmdb"
	"github.com/stupside/castor/cmd/castor/internal/cast"
	"github.com/stupside/castor/cmd/castor/internal/sources"
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

// browsable refuses an interactive cast that has no catalog to browse.
func (c Config) browsable() error {
	if c.TMDB.APIKey == "" {
		return errors.New("TMDB API key missing: set tmdb.api_key in config.yaml or CASTOR_TMDB__API_KEY env var")
	}
	return nil
}
