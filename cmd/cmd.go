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
	"google.golang.org/protobuf/proto"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/api/client"
	"github.com/stupside/castor/internal/api/server"
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
			a.apiCommand(),
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

// dial is a client of the server every cast command drives: one in this process, or the one config names.
func dial(ctx context.Context, cfg *config.Config) (*client.Client, error) {
	if !cfg.API.Embedded() {
		return client.New(cfg.API.Endpoint, cfg.LAN()), nil
	}
	// The embedded engine writes nothing here itself: its lines arrive through the watch, as a remote server's do.
	slog.SetDefault(slog.New(server.Logs(slog.Default().Handler(), slog.DiscardHandler)))
	base, err := server.Embedded(ctx, backend(cfg))
	if err != nil {
		return nil, err
	}
	return client.New(base, cfg.LAN()), nil
}

// cast starts the cast req asks for, watches it, and lends it target; cancelling ctx stops it.
func (a *app) cast(ctx context.Context, c *client.Client, req *castorv1.StartCastRequest, target device.Info) error {
	id, err := c.Start(ctx, req)
	if err != nil {
		return err
	}
	// The engine's own lines are detail: shown under --debug only, marked as the server's.
	var engine slog.Handler
	if a.debug {
		engine = slog.Default().Handler().WithAttrs([]slog.Attr{slog.String("from", "server")})
	}
	// Watched before the device is lent: the cast starts with it, so nothing it says goes unseen.
	w, err := c.Watch(ctx, id, &logged{ctx: ctx}, engine)
	if err != nil {
		stop(ctx, c, id)
		return err
	}
	driving := make(chan error, 1)
	go func() { driving <- c.Drive(ctx, id, target) }()

	err = w.Outcome()
	if ctx.Err() != nil {
		stop(ctx, c, id)
		err = context.Cause(ctx)
	}
	// The renderer is released before castor exits, whatever the outcome.
	if derr := <-driving; derr != nil {
		slog.DebugContext(ctx, "driving ended", "error", derr)
	}
	return err
}

func stop(ctx context.Context, c *client.Client, id string) {
	if err := c.Stop(ctx, id); err != nil {
		slog.DebugContext(ctx, "stopping cast", "error", err)
	}
}

// logged shows what changed in a cast's status as log lines.
type logged struct {
	ctx  context.Context
	last *castorv1.CastStatus
}

// Status reports every change it sees: a watcher may get two at once, merged into one status.
func (l *logged) Status(s *castorv1.CastStatus) {
	measuring := castorv1.CastStatus_PHASE_MEASURING
	if s.GetPhase() == measuring && l.last.GetPhase() != measuring {
		slog.InfoContext(l.ctx, "cast measuring", "streams", s.GetStreams())
	}
	if s.GetCastable() != l.last.GetCastable() {
		slog.InfoContext(l.ctx, "cast measured", "castable", s.GetCastable())
	}
	if !proto.Equal(s.GetRevision(), l.last.GetRevision()) {
		slog.WarnContext(l.ctx, "cast revising", "strategy", s.GetRevision().GetStrategy(), "why", s.GetRevision().GetWhy())
	}
	if s.GetAttempt() != l.last.GetAttempt() {
		slog.InfoContext(l.ctx, "cast attempting", "try", s.GetAttempt())
	}
	l.last = s
}

func backend(cfg *config.Config) server.Backend {
	return cfg.Backend(burnIn)
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
