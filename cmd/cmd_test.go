package cmd

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// urfave/cli resolves flags by lineage, so --dry-run works whether typed before or after the subcommand.
func TestDryRunIsBoundWhicheverSideOfTheSubcommandItIsTyped(t *testing.T) {
	const link = "https://cdn.example/hls/index.m3u8"

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"typed after the subcommand, resolved up the lineage", []string{"castor", "cast", "url", link, "--dry-run"}},
		{"the short alias, on the far side of the subcommand", []string{"castor", "cast", "url", link, "-d"}},
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

func TestDryRunLabelsTheRowsCastorIsGuessingAt(t *testing.T) {
	link := func(raw string, bandwidth int64, lastResort bool) *source.Candidate {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return &source.Candidate{URL: u, Probe: &media.ProbeInfo{BitRate: bandwidth}, LastResort: lastResort}
	}

	measured := dryRunRow(link("https://cdn.example/feature.m3u8", 6_000_000, false))
	if measured != "6000000\thttps://cdn.example/feature.m3u8" {
		t.Errorf("measured row = %q, want the bandwidth and the link only", measured)
	}

	guess := dryRunRow(link("https://cdn.example/unproven.mp4", 0, true))
	if !strings.Contains(guess, "last resort") {
		t.Errorf("last-resort row = %q, want it marked apart from a measured link", guess)
	}
}
