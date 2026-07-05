package cmd

import (
	"context"

	"github.com/urfave/cli/v3"
)

func (a *app) castEpisodeCommand() *cli.Command {
	var season int
	var episode int
	var itemID string

	return &cli.Command{
		Name:  "episode",
		Usage: "Cast a series episode by item ID",
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:        "season",
				Usage:       "Season number",
				Required:    true,
				Destination: &season,
			},
			&cli.IntFlag{
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
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}

			return a.extractAndCast(ctx, cmd, cfg.AllEpisodeURLs(itemID, uint(season), uint(episode)))
		},
	}
}
