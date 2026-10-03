package dlna

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"

	"github.com/huin/goupnp"
	"github.com/huin/goupnp/soap"

	"github.com/stupside/castor/services/apiserver/internal/device"
)

// actionTimeout keeps an unresponsive device from wedging an AVTransport action.
const actionTimeout = 10 * time.Second

// session is a device driven over AVTransport with a generic client, since goupnp av1 hardcodes the :1 URN.
type session struct {
	transport goupnp.ServiceClient
	caps      *mediav1.Capabilities
}

func (s *session) Capabilities() *mediav1.Capabilities { return s.caps }

// name is the device's FriendlyName, else its location.
func (s *session) name() string {
	var friendly, location string
	if s.transport.RootDevice != nil {
		friendly = s.transport.RootDevice.Device.FriendlyName
	}
	if s.transport.Location != nil {
		location = s.transport.Location.String()
	}
	return cmp.Or(friendly, location, "DLNA device")
}

var _ device.Device = (*session)(nil)

const (
	transportLockedRetries = 5
	transportLockedDelay   = 300 * time.Millisecond
)

// Play hands over one video resource with no caption track: subtitles are burned in upstream.
func (s *session) Play(ctx context.Context, streamURL *url.URL, container mediav1.Container) error {
	metadata, err := buildDIDLMetadata(streamURL, container)
	if err != nil {
		return fmt.Errorf("building DIDL-Lite metadata: %w", err)
	}
	slog.DebugContext(ctx, "DIDL metadata", "xml", metadata)

	setURI := &struct {
		InstanceID         string
		CurrentURI         string
		CurrentURIMetaData string
	}{"0", streamURL.String(), metadata}
	if err := retryTransportLocked(ctx, func() error {
		return s.action(ctx, "SetAVTransportURI", setURI)
	}); err != nil {
		return fmt.Errorf("setting transport URI: %w", err)
	}

	play := &struct {
		InstanceID string
		Speed      string
	}{"0", "1"}
	if err := retryTransportLocked(ctx, func() error {
		return s.action(ctx, "Play", play)
	}); err != nil {
		return fmt.Errorf("starting playback: %w", err)
	}
	return nil
}

func retryTransportLocked(ctx context.Context, do func() error) error {
	var err error
	for attempt := range transportLockedRetries {
		err = do()
		if !isTransportLocked(err) {
			return err
		}
		slog.DebugContext(ctx, "transport locked, retrying action", "attempt", attempt+1)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(transportLockedDelay):
		}
	}
	return err
}

func isTransportLocked(err error) bool {
	fault, ok := errors.AsType[*soap.SOAPFaultError](err)
	return ok && fault.Detail.UPnPError.Errorcode == 705
}

// action performs a SOAP action namespaced to the service version the device published.
func (s *session) action(ctx context.Context, name string, request any) error {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	return s.transport.SOAPClient.PerformActionCtx(
		ctx, s.transport.Service.ServiceType, name, request, nil)
}

const transportQuery = "GetTransportInfo"

// transportWatch holds the one thing that ends a cast from the device's side.
type transportWatch struct {
	active bool
}

func (w *transportWatch) observe(state string) bool {
	switch state {
	case "PLAYING", "PAUSED_PLAYBACK", "RECORDING":
		w.active = true
	case "STOPPED", "NO_MEDIA_PRESENT":
		return w.active
	}
	return false
}

// AwaitEnd polls AVTransport because UPnP families share no dependable event subscription.
func (s *session) AwaitEnd(ctx context.Context) error {
	return awaitTransportEnd(ctx, s.name(), s.transportState)
}

// awaitTransportEnd folds the AVTransport state machine over the shared poll loop.
func awaitTransportEnd(ctx context.Context, name string, poll func(context.Context) (string, error)) error {
	var watch transportWatch
	return device.AwaitPolledEnd(ctx, name, transportQuery,
		func(ctx context.Context) (bool, error) {
			state, err := poll(ctx)
			if err != nil {
				return false, err
			}
			return watch.observe(state), nil
		})
}

func (s *session) transportState(ctx context.Context) (string, error) {
	var response struct {
		CurrentTransportState string
	}
	err := s.transport.SOAPClient.PerformActionCtx(ctx, s.transport.Service.ServiceType,
		transportQuery, &struct{ InstanceID string }{"0"}, &response)
	if err != nil {
		return "", err
	}
	return response.CurrentTransportState, nil
}

func (s *session) Close() error {
	return nil
}
