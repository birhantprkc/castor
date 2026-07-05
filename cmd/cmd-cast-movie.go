package cmd

import (
	"context"

	"github.com/urfave/cli/v3"
)

func (a *app) castMovieCommand() *cli.Command {
	var itemID string

	return &cli.Command{
		Name:  "movie",
		Usage: "Cast a movie by item ID",
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

			return a.extractAndCast(ctx, cmd, cfg.AllMovieURLs(itemID))
		},
	}
}
