package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/cmd/internal/process"
	"github.com/stupside/castor/internal/transport"
)

// urfave/cli resolves flags by lineage, so --dry-run works whether typed before or after the subcommand.
func TestADryRunOfALinkNeedsNoConfigAndNoServerWhicheverSideTheFlagIsTyped(t *testing.T) {
	const link = "https://cdn.example/hls/index.m3u8"
	refuse := Local(func(context.Context, *cli.Command, slog.Handler) (transport.Endpoint, bool, func(), error) {
		return transport.Endpoint{}, false, nil, errors.New("a dry run started castor's servers")
	})
	for _, args := range [][]string{
		{"castor", "-c", "absent.yaml", "cast", "--dry-run", "url", link},
		{"castor", "-c", "absent.yaml", "cast", "url", link, "--dry-run"},
	} {
		root := &cli.Command{Name: "castor", Flags: process.Flags, Commands: Commands(refuse)}
		if err := root.Run(t.Context(), args); err != nil {
			t.Errorf("%v: %v; a dry run of a link must not read a config or start a server", args, err)
		}
	}
}
