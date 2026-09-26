package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/browse"
	"github.com/stupside/castor/internal/browse/picker"
	"github.com/stupside/castor/internal/browse/tmdb"
	"github.com/stupside/castor/internal/cast"
	"github.com/stupside/castor/internal/config"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/source"
)

func (a *app) castCommand() *cli.Command {
	return &cli.Command{
		Name:  "cast",
		Usage: "Browse and cast to a device",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:        "dry-run",
				Aliases:     []string{"d"},
				Usage:       "Print found streaming URLs instead of casting",
				Destination: &a.dryRun,
			},
		},
		Action: a.castInteractive,
		Commands: []*cli.Command{
			a.castURLCommand(),
			a.castMovieCommand(),
			a.castEpisodeCommand(),
			a.castPlayerCommand(),
		},
	}
}

func (a *app) castInteractive(ctx context.Context, _ *cli.Command) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}

	// Check config early to avoid discovery sweep if TMDB key is missing.
	if cfg.TMDB.APIKey == "" {
		return fmt.Errorf("TMDB API key missing: set tmdb.api_key in config.yaml or CASTOR_TMDB__API_KEY env var")
	}

	discover := func(ctx context.Context) []device.Info {
		return cfg.Devices().Discover(ctx, cfg.Network.Timeout)
	}
	target, err := picker.Device(ctx, discover, cfg.Device.Name)
	if err != nil {
		return fmt.Errorf("picking device: %w", err)
	}

	sel, err := browse.Run(ctx, tmdb.New(cfg.TMDB.APIKey), target.Name, target.Type)
	if err != nil {
		return fmt.Errorf("browse: %w", err)
	}
	if sel.Kind == browse.KindNone {
		return nil
	}

	var urls []string
	switch sel.Kind {
	case browse.KindMovie:
		urls = cfg.Sources.MovieURLs(sel.TMDBID)
	case browse.KindEpisode:
		urls = cfg.Sources.EpisodeURLs(sel.TMDBID, sel.Season, sel.Episode)
	}

	fmt.Printf("Casting: %s\n", sel.Title)

	return a.extractAndCast(ctx, cfg, target, urls)
}

// extractAndCast finds the streams on urls, then prints their ranking (-dry-run) or casts the best one to target.
func (a *app) extractAndCast(ctx context.Context, cfg *config.Config, target device.Info, urls []string) error {
	streams, err := cfg.Extractor().ExtractAll(ctx, urls)
	if err != nil {
		return fmt.Errorf("extracting streams: %w", err)
	}
	ranked, err := cfg.Ranker().Rank(ctx, streams)
	if err != nil {
		return fmt.Errorf("ranking streams: %w", err)
	}

	if a.dryRun {
		for _, s := range ranked {
			fmt.Println(dryRunRow(s))
		}
		return nil
	}

	// Pass all candidates; casting falls back to next if best fails instead of failing.
	return cast.Play(ctx, playback(cfg, target), ranked)
}

// dryRunRow formats a stream as bandwidth and URL, with "last resort" label if unmeasured.
func dryRunRow(s *source.Candidate) string {
	row := fmt.Sprintf("%d\t%s", s.Bitrate(), s.URL)
	if s.LastResort {
		row += "\tlast resort"
	}
	return row
}
