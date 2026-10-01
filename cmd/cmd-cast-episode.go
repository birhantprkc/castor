package cmd

import (
	"context"

	"github.com/urfave/cli/v3"
)

func (a *app) castEpisodeCommand() *cli.Command {
	var season uint
	var episode uint
	var itemID string

	return &cli.Command{
		Name:  "episode",
		Usage: "Cast a series episode by item ID",
		Flags: []cli.Flag{
			&cli.UintFlag{
				Name:        "season",
				Usage:       "Season number",
				Required:    true,
				Destination: &season,
			},
			&cli.UintFlag{
				Name:        "episode",
				Usage:       "Episode number",
				Required:    true,
				Destination: &episode,
			},
		},
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:        "itemID",
				Destination: &itemID,
			},
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}

			target, err := cfg.Target()
			if err != nil {
				return err
			}
			return a.extractAndCast(ctx, cfg, target, cfg.Sources.EpisodeURLs(itemID, season, episode))
		},
	}
}
