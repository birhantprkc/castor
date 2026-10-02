package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/urfave/cli/v3"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/config"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/device/picker"
	"github.com/stupside/castor/internal/titles/browse"
	"github.com/stupside/castor/internal/titles/tmdb"
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
		return errors.New("TMDB API key missing: set tmdb.api_key in config.yaml or CASTOR_TMDB__API_KEY env var")
	}

	discover := func(ctx context.Context) []device.Info {
		return cfg.Devices().Discover(ctx, cfg.Network.Timeout)
	}
	var target device.Info
	var sel browse.Selection
	err = held(func() (err error) {
		if target, err = picker.Device(ctx, discover, cfg.Device.Name); err != nil {
			return fmt.Errorf("picking device: %w", err)
		}
		if sel, err = browse.Run(ctx, tmdb.New(cfg.TMDB.APIKey), target.Name, target.Type); err != nil {
			return fmt.Errorf("browse: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
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

// extractAndCast finds the streams on urls, then prints their ranking (-dry-run) or has the server cast them to target.
func (a *app) extractAndCast(ctx context.Context, cfg *config.Config, target device.Info, urls []string) error {
	c, err := a.dial(ctx, cfg)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "finding streams", "pages", len(urls))
	streams, err := c.Extract(ctx, &castorv1.ExtractRequest{Pages: urls})
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "streams found", "count", len(streams))
	if a.dryRun {
		// Ranked where it would be cast, by the server, as this cast would ask.
		ranked, err := c.Rank(ctx, &castorv1.RankRequest{Streams: streams, Preferences: cfg.Preferences()})
		if err != nil {
			return err
		}
		for _, s := range ranked {
			fmt.Println(dryRunRow(s))
		}
		return nil
	}
	// Pass all streams; the server ranks them and falls back to the next if the best fails.
	return a.cast(ctx, cfg, c, &castorv1.StartRequest{
		Streams:     &castorv1.StartRequest_Found{Found: &castorv1.StartRequest_Streams{Streams: streams}},
		Preferences: cfg.Preferences(),
	}, target)
}

// dryRunRow formats a stream as bandwidth and URL, with "last resort" label if unmeasured.
func dryRunRow(s *castorv1.RankedStream) string {
	row := fmt.Sprintf("%d\t%s", s.GetBitrate(), s.GetUrl())
	if s.GetLastResort() {
		row += "\tlast resort"
	}
	return row
}
