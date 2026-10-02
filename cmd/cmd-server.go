package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/api/server"
)

func (a *app) serverCommand() *cli.Command {
	return &cli.Command{
		Name:  "server",
		Usage: "Run casts for castor on other machines, and serve their TVs, until interrupted",
		Action: func(ctx context.Context, _ *cli.Command) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			l, err := net.Listen("tcp", cfg.Server.Listen)
			if err != nil {
				return fmt.Errorf("listening on %s: %w", cfg.Server.Listen, err)
			}
			// A detached server keeps every line on its own output too; its clients get their casts' lines live.
			h := slog.Default().Handler()
			slog.SetDefault(slog.New(server.Logs(h, h)))
			advertised, err := cfg.Advertised(ctx, l)
			if err != nil {
				return fmt.Errorf("resolving where TVs reach this server (set server.advertise): %w", err)
			}
			return server.Serve(ctx, l, advertised, backend(cfg))
		},
	}
}
