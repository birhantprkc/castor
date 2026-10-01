// Package client drives a castor server for the renderers on its own network: it finds, controls and relays to them.
package client

import (
	"context"
	"net/http"

	"github.com/stupside/castor/gen/castor/v1/castorv1connect"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

// LAN is the client's side of a cast: the renderers it reaches, and the address they reach it at.
type LAN struct {
	Renderers Renderers
	Addresses Addresses
}

// Renderers reaches the renderers on the client's network.
type Renderers interface {
	Profile(t device.Type) media.Capabilities
	Connect(ctx context.Context, target device.Info) (device.Device, error)
}

// Addresses is the client's LAN address, which renderers fetch relayed media from.
type Addresses interface {
	LocalIPv4(ctx context.Context) (string, error)
}

// Client drives casts on a castor server, local or remote, for the renderers on its own network.
type Client struct {
	base    string
	lan     LAN
	casts   castorv1connect.CastServiceClient
	devices castorv1connect.DeviceServiceClient
	logger  castorv1connect.LoggerServiceClient
	streams castorv1connect.StreamServiceClient
	// extractor is the server's browser, which finds the streams pages play.
	extractor castorv1connect.ExtractServiceClient
}

func New(baseURL string, lan LAN) *Client {
	return &Client{
		base:      baseURL,
		lan:       lan,
		casts:     castorv1connect.NewCastServiceClient(http.DefaultClient, baseURL),
		devices:   castorv1connect.NewDeviceServiceClient(http.DefaultClient, baseURL),
		logger:    castorv1connect.NewLoggerServiceClient(http.DefaultClient, baseURL),
		streams:   castorv1connect.NewStreamServiceClient(http.DefaultClient, baseURL),
		extractor: castorv1connect.NewExtractServiceClient(http.DefaultClient, baseURL),
	}
}
