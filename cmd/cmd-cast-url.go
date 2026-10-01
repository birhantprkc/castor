package cmd

import (
	"context"
	"fmt"
	"net/url"

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
			urlObj, err := url.Parse(urlArg)
			if err != nil {
				return fmt.Errorf("invalid URL %q: %w", urlArg, err)
			}

			if a.dryRun {
				fmt.Println(urlObj.String())
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
			return a.cast(ctx, cfg, c, &castorv1.StartCastRequest{
				Streams:     &castorv1.StartCastRequest_Named{Named: &castorv1.Stream{Url: urlObj.String()}},
				Preferences: cfg.Preferences(),
			}, target)
		},
	}
}
