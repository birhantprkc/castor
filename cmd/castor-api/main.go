// Command castor-api serves castor's API on the devices' network, casting through the media server server.url names; it links no cgo.
package main

import (
	"github.com/urfave/cli/v3"

	"github.com/stupside/castor/cmd/internal/process"
	"github.com/stupside/castor/services/apiserver"
)

func main() {
	api := apiserver.Command(apiserver.RemoteMedia)
	process.Run(&cli.Command{Name: "castor-api", Usage: api.Usage, Action: api.Action})
}
