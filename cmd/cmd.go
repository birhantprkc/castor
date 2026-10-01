// Package cmd wires the castor command tree with lazy config loading for commands that don't need it.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"charm.land/log/v2"
	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/cast"
	"github.com/stupside/castor/internal/cast/execute"
	"github.com/stupside/castor/internal/config"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/subtitle"
	"github.com/stupside/castor/internal/subtitle/whisper"
	"github.com/stupside/castor/internal/version"
)

type app struct {
	configPath string
	configSet  bool
	debug      bool
	dryRun     bool

	config func() (*config.Config, error)
}

func Root() *cli.Command {
	a := &app{}
	a.config = sync.OnceValues(func() (*config.Config, error) {
		// The default path may be absent (env and defaults suffice); a path the user named may not.
		if a.configSet {
			if _, err := os.Stat(a.configPath); err != nil {
				return nil, fmt.Errorf("config file: %w", err)
			}
		}
		cfg, err := config.Load(a.configPath)
		if err == nil {
			slog.Info("config loaded", "path", a.configPath)
		}
		return cfg, err
	})

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
				Destination: &a.configPath,
			},
			&cli.BoolFlag{
				Name:        "debug",
				Usage:       "Enable debug logging",
				Destination: &a.debug,
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			a.configSet = cmd.IsSet("config")
			if a.debug {
				slog.SetDefault(slog.New(
					log.NewWithOptions(os.Stderr, log.Options{
						ReportTimestamp: true,
						TimeFormat:      "15:04:05.000",
						Level:           log.DebugLevel,
					}),
				))
			}
			return ctx, nil
		},
		Commands: []*cli.Command{
			a.castCommand(),
			a.scanCommand(),
			infoCommand(),
		},
	}
}

func infoCommand() *cli.Command {
	return &cli.Command{
		Name:  "info",
		Usage: "Print build information",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			fmt.Printf("version    %s\n", version.Version)
			fmt.Printf("commit     %s\n", version.Commit)
			fmt.Printf("build time %s\n", version.BuildTime)
			return nil
		},
	}
}

func playback(cfg *config.Config, target device.Info) cast.Config {
	return cfg.Playback(target, burnIn(cfg.Whisper))
}

func burnIn(settings subtitle.Whisper) execute.Subtitles {
	if !settings.Enable {
		return nil
	}
	return func(ctx context.Context, workDir string) execute.Burn {
		b, err := whisper.New(ctx, settings, workDir)
		if err != nil {
			slog.WarnContext(ctx, "whisper init failed; casting without subtitles", "error", err)
			return nil
		}
		return b
	}
}
