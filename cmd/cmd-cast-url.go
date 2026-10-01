package cmd

import (
	"context"
	"fmt"
	"net/url"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/cast"
	"github.com/stupside/castor/internal/source"
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

			// Measure direct URL (not rank) to extract envelope for pass-through.
			stream := &source.Stream{URL: urlObj, ContentType: cfg.Identify(ctx, urlObj)}
			measured, err := cfg.Ranker().Measure(ctx, stream)
			if err != nil {
				return fmt.Errorf("measuring direct URL: %w", err)
			}
			return cast.Play(ctx, playback(cfg, cfg.Target()), []*source.Stream{measured})
		},
	}
}
