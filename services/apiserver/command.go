package apiserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/settings"
	"github.com/stupside/castor/internal/transport"
)

// Media runs a media server in this process, for an API server that names none, its own lines going to lines.
type Media func(ctx context.Context, cmd *cli.Command, lines slog.Handler) (transport.Endpoint, func(), error)

// RemoteMedia is the Media of a binary that links no media server, so server.url must name one.
func RemoteMedia(context.Context, *cli.Command, slog.Handler) (transport.Endpoint, func(), error) {
	return transport.Endpoint{}, nil, errors.New("server.url is required: this binary runs no media server of its own")
}

// Command is `castor api`: the API server, casting through the media server it names or one it runs, until interrupted.
func Command(media Media) *cli.Command {
	return &cli.Command{
		Name:  "api",
		Usage: "Serve castor's API, casting on the devices on this network, until interrupted",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg, err := settings.Load(cmd, defaults())
			if err != nil {
				return err
			}
			// A media server run here writes its lines as this server's own, as `castor server` does.
			srv, stop, err := cfg.server(ctx, cmd, media, slog.Default().Handler())
			if err != nil {
				return err
			}
			// It outlives the API server, which stops its casts through it.
			defer stop()
			l, err := transport.Listen(ctx, cfg.API.Listen, cfg.API.Token, "api.token")
			if err != nil {
				return err
			}
			slog.InfoContext(ctx, "api serving", "address", l.Addr().String())
			return transport.Serve(ctx, l, transport.Authorized(srv, cfg.API.Token), srv.Shutdown)
		},
	}
}

// Embedded runs an API server in this process until stop, on loopback, its media server's own lines going to lines when it runs one too, which written reports.
func Embedded(media Media) func(ctx context.Context, cmd *cli.Command, lines slog.Handler) (api transport.Endpoint, written bool, stop func(), err error) {
	return func(ctx context.Context, cmd *cli.Command, lines slog.Handler) (transport.Endpoint, bool, func(), error) {
		cfg, err := settings.Load(cmd, defaults())
		if err != nil {
			return transport.Endpoint{}, false, nil, err
		}
		srv, stopMedia, err := cfg.server(ctx, cmd, media, lines)
		if err != nil {
			return transport.Endpoint{}, false, nil, err
		}
		api, stopAPI, err := transport.Loopback(ctx, srv, cfg.API.Token, srv.Shutdown)
		if err != nil {
			stopMedia()
			return transport.Endpoint{}, false, nil, err
		}
		stop := func() {
			stopAPI()
			stopMedia()
		}
		return api, cfg.Server.URL == "", stop, nil
	}
}

// server is the API server cfg describes, casting through the media server it names, or one media runs here; stop ends the one run here.
func (c *Config) server(ctx context.Context, cmd *cli.Command, media Media, lines slog.Handler) (srv *Server, stop func(), err error) {
	at, stop := transport.Endpoint{URL: c.Server.URL, Token: c.Server.Token}, func() {}
	if at.URL == "" {
		if at, stop, err = media(ctx, cmd, lines); err != nil {
			return nil, nil, err
		}
	}
	if srv, err = New(c.backend(at), c.Cast); err != nil {
		stop()
		return nil, nil, fmt.Errorf("validating config: %w", err)
	}
	return srv, stop, nil
}
