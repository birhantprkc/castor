package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/cast"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source/extract"
	"github.com/stupside/castor/internal/source/resolve"
)

func (a *app) castCommand() *cli.Command {
	return &cli.Command{
		Name:  "cast",
		Usage: "Cast a video to the default device",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "dry-run",
				Aliases: []string{"d"},
				Usage:   "Print found streaming URLs instead of casting",
			},
		},
		Commands: []*cli.Command{
			a.castURLCommand(),
			a.castMovieCommand(),
			a.castEpisodeCommand(),
			a.castPlayerCommand(),
			a.castBrowseCommand(),
		},
	}
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

// handleStreams handles the --dry-run / cast logic shared by player, movie, and episode commands.
func (a *app) handleStreams(ctx context.Context, cmd *cli.Command, streams []*media.Stream) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}

	if cmd.Bool("dry-run") {
		for _, d := range resolve.ListStreams(ctx, cfg.Resolver, streams) {
			fmt.Printf("%d\t%s\n", d.BitRate, d.URL)
		}
		return nil
	}

	best, err := resolve.RankStreams(ctx, cfg.Resolver, streams)
	if err != nil {
		return fmt.Errorf("ranking streams: %w", err)
	}

	return cast.Play(ctx, cfg.Cast(), best)
}
