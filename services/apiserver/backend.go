package apiserver

import (
	"github.com/stupside/castor/internal/transport"
	"github.com/stupside/castor/services/apiserver/internal/device"
	"github.com/stupside/castor/services/apiserver/internal/device/chromecast"
	"github.com/stupside/castor/services/apiserver/internal/device/dlna"
	"github.com/stupside/castor/services/apiserver/internal/device/roku"
	"github.com/stupside/castor/services/apiserver/internal/mediaclient"
)

// backend is what the API server casts with, bound to c: every device family castor casts to, and the media server at media.
func (c *Config) backend(media transport.Endpoint) Backend {
	return Backend{
		Devices: device.Registry{
			Families: []device.Family{dlna.Family{}, chromecast.Family{}, roku.Family{Config: c.Devices.Roku}},
			Timeout:  c.Network.Timeout,
		},
		Media: mediaclient.New(media.Client(), media.URL),
	}
}
