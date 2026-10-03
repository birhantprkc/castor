// Command castor casts video streams to the devices on a network: it gathers each tier's commands, and runs the tiers together when no server is named.
package main

import (
	"slices"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/internal/process"
	"github.com/stupside/castor/services/apiserver"
	"github.com/stupside/castor/services/mediaserver"
	"github.com/stupside/castor/tui"
)

func main() {
	media := apiserver.Media(mediaserver.Embedded)
	local := tui.Local(apiserver.Embedded(media))
	process.Run(&cli.Command{
		Name:     "castor",
		Usage:    "Cast video streams to networked devices",
		Commands: slices.Concat(tui.Commands(local), []*cli.Command{apiserver.Command(media), mediaserver.Command()}),
	})
}
