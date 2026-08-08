package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/browse"
	"github.com/stupside/castor/internal/browse/tmdb"
	"github.com/stupside/castor/internal/cast"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source/extract"
)

func (a *app) castCommand() *cli.Command {
	return &cli.Command{
		Name:  "cast",
		Usage: "Browse and cast to a device",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "dry-run",
				Aliases: []string{"d"},
				Usage:   "Print found streaming URLs instead of casting",
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

func (a *app) castInteractive(ctx context.Context, cmd *cli.Command) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}

	devInfo, err := browse.PickDevice(cfg.Network.Timeout, cfg.Device.Name)
	if err != nil {
		return fmt.Errorf("picking device: %w", err)
	}
	// The picker lists devices of every family, so the pick can differ from
	// config.yaml's device.type; propagate all three fields, not just Name, or
	// DeviceConfig.resolve() reconnects using the stale configured type/host
	// instead of the device the user just selected.
	cfg.Device.Name = devInfo.Name
	cfg.Device.Type = devInfo.Type
	cfg.Device.Host = devInfo.Address

	if cfg.TMDB.APIKey == "" {
		return fmt.Errorf("TMDB API key missing: set tmdb.api_key in config.yaml or CASTOR_TMDB__API_KEY env var")
	}

	sel, err := browse.Run(ctx, tmdb.New(cfg.TMDB.APIKey, cfg.Network.Timeout), devInfo.Name, devInfo.Type)
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
}

// extractAndCast creates an extractor, extracts streams from the given URLs,
// and either lists them (--dry-run) or casts the best one.
func (a *app) extractAndCast(ctx context.Context, cmd *cli.Command, urls []string) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}

	ext, err := extract.New(cfg.Extractor())
	if err != nil {
		return fmt.Errorf("creating extractor: %w", err)
	}

	streams, err := ext.ExtractAll(ctx, urls)
	if err != nil {
		return fmt.Errorf("extracting streams: %w", err)
	}

	return a.handleStreams(ctx, cmd, streams)
}

// handleStreams handles the --dry-run / cast logic shared by player, movie, and
// episode commands. Both paths run the one ranking: --dry-run prints the ordering a
// cast would walk, head first, rather than a listing of its own. The listing it
// replaces probed a different set (every variant of every candidate, in extraction
// order, with no admission rule applied), so it could and did show a stream the cast
// would never have chosen, and showed nothing about the ones it rejected.
func (a *app) handleStreams(ctx context.Context, cmd *cli.Command, streams []*media.Stream) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}

	ranked, err := cfg.Source().RankStreams(ctx, streams)
	if err != nil {
		return fmt.Errorf("ranking streams: %w", err)
	}

	if cmd.Bool("dry-run") {
		for _, s := range ranked {
			fmt.Printf("%d\t%s\n", s.Bandwidth, s.URL)
		}
		return nil
	}

	// The whole ordering, not its head. Ranking measured every one of these links and
	// ordered them for exactly this: a cast that cannot get the bytes out of the best
	// candidate can fall to the next one instead of failing the title. Handing over one
	// link is what left a run holding four alternatives, two of which probed cleanly,
	// with nothing to do about a head that delivered 33 KB in thirty seconds.
	return cast.Play(ctx, cfg.Playback(), ranked)
}
