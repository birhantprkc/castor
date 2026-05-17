package cmd

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/app"
	"github.com/stupside/castor/internal/version"
)

// Root returns the root CLI command.
func Root() *cli.Command {
	var configPath string

	return &cli.Command{
		Name:    "castor",
		Usage:   "Cast video streams to networked devices",
		Version: version.Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "config",
				Aliases:     []string{"c"},
				Usage:       "Path to configuration file",
				Value:       "config.yaml",
				Destination: &configPath,
			},
			&cli.BoolFlag{
				Name:  "debug",
				Usage: "Enable debug logging",
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			cfg, err := app.Load(configPath)
			if err != nil {
				return ctx, err
			}
			slog.InfoContext(ctx, "config loaded", "path", configPath)
			cmd.Metadata["config"] = cfg
			return ctx, nil
		},
		Commands: []*cli.Command{
			castCommand(),
			scanCommand(),
			{
				Name:  "info",
				Usage: "Print build information",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					fmt.Printf("version    %s\n", version.Version)
					fmt.Printf("commit     %s\n", version.Commit)
					fmt.Printf("build time %s\n", version.BuildTime)
					return nil
				},
			},
		},
		Metadata: map[string]any{},
	}
}
