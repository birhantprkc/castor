// Command castor is castor's command line: it casts over castor's public API, and runs both servers in its own process when no server is named.
package main

import (
	"slices"

	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/cmd/internal/process"
	"github.com/stupside/castor/services/apiserver"
	"github.com/stupside/castor/services/mediaserver"
)

func main() {
	media := apiserver.Media(mediaserver.Embedded)
	local := Local(apiserver.Embedded(media))
	process.Run(&cli.Command{
		Name:     "castor",
		Usage:    "Cast video streams to networked devices",
		Commands: slices.Concat(Commands(local), []*cli.Command{apiserver.Command(media), mediaserver.Command()}),
	})
}
