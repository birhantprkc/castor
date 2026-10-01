package cmd

import (
	"context"

	"github.com/urfave/cli/v3"
)

func (a *app) castPlayerCommand() *cli.Command {
	var pageURL string

	return &cli.Command{
		Name:  "player",
		Usage: "Cast a video from a direct player URL",
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:        "url",
				Destination: &pageURL,
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
			return a.extractAndCast(ctx, cfg, target, []string{pageURL})
		},
	}
}
