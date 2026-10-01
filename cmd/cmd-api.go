package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/api/server"
)

func (a *app) apiCommand() *cli.Command {
	return &cli.Command{
		Name:  "api",
		Usage: "Serve castor's API to other frontends",
		Commands: []*cli.Command{
			{
				Name:  "server",
				Usage: "Do the heavy work of casts on api.listen until interrupted",
				Action: func(ctx context.Context, _ *cli.Command) error {
					cfg, err := a.config()
					if err != nil {
						return err
					}
					l, err := net.Listen("tcp", cfg.API.Listen)
					if err != nil {
						return fmt.Errorf("listening on %s: %w", cfg.API.Listen, err)
					}
					// A detached server keeps every line on its own output too; its clients get their casts' lines live.
					h := slog.Default().Handler()
					slog.SetDefault(slog.New(server.Logs(h, h)))
					return server.Serve(ctx, l, backend(cfg))
				},
			},
		},
	}
}
