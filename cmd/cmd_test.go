package cmd

import (
	"context"
	"testing"

	"github.com/urfave/cli/v3"
)

// urfave/cli resolves flags by lineage, so --dry-run works whether typed before or after the subcommand.
func TestDryRunIsBoundWhicheverSideOfTheSubcommandItIsTyped(t *testing.T) {
	const link = "https://cdn.example/hls/index.m3u8"

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"typed before the subcommand", []string{"castor", "cast", "--dry-run", "url", link}},
		{"typed after the subcommand, resolved up the lineage", []string{"castor", "cast", "url", link, "--dry-run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &app{}
			root := &cli.Command{Name: "castor", Commands: []*cli.Command{a.castCommand()}}

			if err := root.Run(context.Background(), tc.args); err != nil {
				t.Fatalf("run: %v; a dry run must not need a config, a device or an origin", err)
			}
			if !a.dryRun {
				t.Error("the dry-run destination was not set, so this invocation would have cast for real")
			}
		})
	}
}
