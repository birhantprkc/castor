// Package process is what every castor binary runs on: its signals, its logger, its global flags and its build identity.
package process

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"charm.land/log/v2"
	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/settings"
)

// version is stamped at link time; the commit and its time come from the build's VCS stamp.
var version = "dev"

const debugFlag = "debug"

// Flags are the root flags every castor binary reads its configuration and verbosity through.
var Flags = []cli.Flag{settings.Flag, &cli.BoolFlag{Name: debugFlag, Usage: "Enable debug logging"}}

// Debug reports whether cmd runs under --debug.
func Debug(cmd *cli.Command) bool { return cmd.Bool(debugFlag) }

// Run runs root until it returns or the process is interrupted, exiting non-zero on its error.
func Run(root *cli.Command) {
	slog.SetDefault(logger(log.InfoLevel))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal asks for a clean stop; restoring the default lets a second one end the process at once.
	context.AfterFunc(ctx, stop)

	root.Version = version
	root.Flags = append(root.Flags, Flags...)
	root.Before = func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		if Debug(cmd) {
			slog.SetDefault(logger(log.DebugLevel))
		}
		return ctx, nil
	}
	root.Commands = append(root.Commands, info())

	if err := root.Run(ctx, os.Args); err != nil {
		if cause := context.Cause(ctx); cause != nil {
			slog.InfoContext(ctx, "shutting down", "cause", cause)
			return
		}
		slog.ErrorContext(ctx, "application error", "error", err)
		os.Exit(1)
	}
}

func info() *cli.Command {
	return &cli.Command{
		Name:  "info",
		Usage: "Print build information",
		Action: func(context.Context, *cli.Command) error {
			commit, built := "none", "unknown"
			if info, ok := debug.ReadBuildInfo(); ok {
				for _, s := range info.Settings {
					switch s.Key {
					case "vcs.revision":
						commit = s.Value
					case "vcs.time":
						built = s.Value
					}
				}
			}
			fmt.Printf("version    %s\n", version)
			fmt.Printf("commit     %s\n", commit)
			fmt.Printf("build time %s\n", built)
			return nil
		},
	}
}

func logger(level log.Level) *slog.Logger {
	return slog.New(log.NewWithOptions(os.Stderr, log.Options{
		ReportTimestamp: true,
		TimeFormat:      "15:04:05.000",
		Level:           level,
	}))
}
