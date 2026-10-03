// Command castor-media runs castor's media server alone, for the API servers that reach it and their devices.
package main

import (
	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/cmd/internal/process"
	"github.com/stupside/castor/services/mediaserver"
)

func main() {
	media := mediaserver.Command()
	process.Run(&cli.Command{Name: "castor-media", Usage: media.Usage, Action: media.Action})
}
