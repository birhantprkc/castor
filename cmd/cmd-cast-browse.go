package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/browse"
	"github.com/stupside/castor/internal/browse/tmdb"
)

func (a *app) castBrowseCommand() *cli.Command {
	return &cli.Command{
		Name:  "browse",
		Usage: "Browse TMDB in a TUI, then cast the selection",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}

			if cfg.TMDB.APIKey == "" {
				return fmt.Errorf("TMDB API key missing: set tmdb.api_key in config.yaml or CASTOR_TMDB__API_KEY env var")
			}

			sel, err := browse.Run(ctx, tmdb.New(cfg.TMDB.APIKey, cfg.Network.Timeout))
			if err != nil {
				return fmt.Errorf("browse: %w", err)
			}
			if sel.Kind == browse.KindNone {
				return nil
			}

			var urls []string
			switch sel.Kind {
			case browse.KindMovie:
				urls = cfg.AllMovieURLs(sel.TMDBID)
			case browse.KindEpisode:
				urls = cfg.AllEpisodeURLs(sel.TMDBID, sel.Season, sel.Episode)
			}

			fmt.Printf("Casting: %s\n", sel.Title)
			return a.extractAndCast(ctx, cmd, urls)
		},
	}
}
