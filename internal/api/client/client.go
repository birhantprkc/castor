// Package client lends a castor server the renderers on its own network, running the server's calls on them.
package client

import (
	"context"
	"net/http"

	"github.com/stupside/castor/gen/castor/v1/castorv1connect"
	"github.com/stupside/castor/internal/device"
)

// Renderers reaches the renderers on the client's network.
type Renderers interface {
	// SelfFetches is whether renderers of type t fetch a stream URL themselves.
	SelfFetches(t device.Type) bool
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
	return &Client{
		renderers: renderers,
		casts:     castorv1connect.NewCastServiceClient(http.DefaultClient, baseURL),
		devices:   castorv1connect.NewDeviceServiceClient(http.DefaultClient, baseURL),
		logger:    castorv1connect.NewLoggerServiceClient(http.DefaultClient, baseURL),
		streams:   castorv1connect.NewStreamServiceClient(http.DefaultClient, baseURL),
		extractor: castorv1connect.NewExtractServiceClient(http.DefaultClient, baseURL),
	}
}
