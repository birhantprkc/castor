package cmd

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/config"
)

// scanCommand discovers config values without requiring valid config.
func (a *app) scanCommand() *cli.Command {
	return &cli.Command{
		Name:  "scan",
		Usage: "List all devices on the local network",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg, err := a.config()
			if err != nil {
				slog.DebugContext(ctx, "scanning without config", "error", err)
				cfg = config.Defaults()
			}

			devices := cfg.Devices().Discover(ctx, cfg.Network.Timeout)
			if len(devices) == 0 {
				fmt.Println("no devices found")
				return nil
			}

			for _, d := range devices {
				fmt.Printf("%s\t%s\t%s\n", d.Name, d.Type, d.Address)
			}
			return nil
		},
	}
}
