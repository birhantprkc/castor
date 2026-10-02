package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

func (a *app) castURLCommand() *cli.Command {
	var urlArg string

	return &cli.Command{
		Name:  "url",
		Usage: "Cast a direct video URL",
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:        "url",
				Destination: &urlArg,
			},
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			if a.dryRun {
				fmt.Println(urlArg)
				return nil
			}

			cfg, err := a.config()
			if err != nil {
				return err
			}

			target, err := cfg.Target()
			if err != nil {
				return err
			}
			c, err := a.dial(ctx, cfg)
			if err != nil {
				return err
			}
			// Named, not found: the server measures it to extract the envelope for pass-through, without ranking.
			return a.cast(ctx, cfg, c, &castorv1.StartRequest{
				Streams:     &castorv1.StartRequest_Named{Named: &castorv1.Stream{Url: urlArg}},
				Preferences: cfg.Preferences(),
			}, target)
		},
	}
}
