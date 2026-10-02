// Package client drives a castor server for the renderers on its own network: it finds them and controls them.
package client

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/stupside/castor/gen/castor/v1/castorv1connect"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

// Renderers reaches the renderers on the client's network.
type Renderers interface {
	Profile(t device.Type) media.Capabilities
	Connect(ctx context.Context, target device.Info) (device.Device, error)
}

// Client drives casts on a castor server, local or remote, for the renderers on its own network.
type Client struct {
	renderers Renderers
	casts     castorv1connect.CastServiceClient
	devices   castorv1connect.DeviceServiceClient
	logger    castorv1connect.LoggerServiceClient
	streams   castorv1connect.StreamServiceClient
	// extractor is the server's browser, which finds the streams pages play.
	extractor castorv1connect.ExtractServiceClient
}

func New(baseURL string, renderers Renderers) *Client {
	// Both ways, every message is held to the rules the contract states.
	valid := connect.WithInterceptors(validate.NewInterceptor(validate.WithValidateResponses()))
	return &Client{
		renderers: renderers,
		casts:     castorv1connect.NewCastServiceClient(http.DefaultClient, baseURL, valid),
		devices:   castorv1connect.NewDeviceServiceClient(http.DefaultClient, baseURL, valid),
		logger:    castorv1connect.NewLoggerServiceClient(http.DefaultClient, baseURL, valid),
		streams:   castorv1connect.NewStreamServiceClient(http.DefaultClient, baseURL, valid),
		extractor: castorv1connect.NewExtractServiceClient(http.DefaultClient, baseURL, valid),
	}
}
