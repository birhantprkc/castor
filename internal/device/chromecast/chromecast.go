// Package chromecast casts to Google Cast receivers.
package chromecast

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	castmedia "github.com/vishen/go-chromecast/cast"
	castdns "github.com/vishen/go-chromecast/dns"

	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

const (
	chromecastPort = 8009

	defaultMediaReceiver = "CC1AD845"
)

type chromecastDevice struct {
	ch *channel

	watchMu     sync.Mutex
	watch       chromecastPlayback
	finish      sync.Once
	done        chan struct{}
	playbackErr error
}

var _ device.Device = (*chromecastDevice)(nil)

// Family is the Cast strategy.
type Family struct{}

// Type is the name config.yaml selects this family by.
const Type device.Type = "chromecast"

func (Family) Type() device.Type { return Type }

var _ device.Family = Family{}

func (Family) SelfFetches() bool { return true }

func (Family) Connect(ctx context.Context, info device.Info) (device.Device, error) {
	dev := &chromecastDevice{done: make(chan struct{})}
	dialCtx, cancel := context.WithTimeout(ctx, answerWithin)
	defer cancel()
	ch, err := dial(dialCtx, chromecastAddress(info.Address), dev.watchMessage)
	if err != nil {
		return nil, fmt.Errorf("connecting to chromecast: %w", err)
	}
	dev.ch = ch
	if err := ch.send(receiverID, nsConnection, &castmedia.PayloadHeader{Type: msgConnect}); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("connecting to chromecast: %w", err)
	}
	if _, err := dev.receiverStatus(ctx, &castmedia.PayloadHeader{Type: msgGetStatus}); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("connecting to chromecast: %w", err)
	}
	return dev, nil
}

// chromecastAddress completes a bare host with the Cast port.
func chromecastAddress(address string) string {
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	return net.JoinHostPort(strings.Trim(address, "[]"), strconv.Itoa(chromecastPort))
}

// Locate passes the address straight through, Connect already accepting both of its forms.
func (Family) Locate(_ context.Context, address string) (string, error) {
	return address, nil
}

// discover browses mDNS (_googlecast._tcp) until ctx expires.
func (Family) Discover(ctx context.Context) []device.Info {
	entries, err := castdns.DiscoverCastDNSEntries(ctx, nil)
	if err != nil {
		slog.WarnContext(ctx, "chromecast discovery error", "error", err)
		return nil
	}

	var devices []device.Info
	seen := make(map[string]struct{})
	for entry := range entries {
		info, ok := chromecastInfo(entry)
		if !ok {
			continue
		}

		// mDNS re-announces the same device; dedupe by UUID, falling back to the address.
		key := cmp.Or(entry.UUID, info.Address)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		devices = append(devices, info)
	}
	return devices
}

// chromecastInfo reports false when the entry advertises no usable IP address.
func chromecastInfo(entry castdns.CastEntry) (device.Info, bool) {
	var host string
	switch {
	case entry.AddrV4 != nil:
		host = entry.AddrV4.String()
	case entry.AddrV6 != nil:
		host = entry.AddrV6.String()
	default:
		return device.Info{}, false
	}

	address := host
	if entry.Port > 0 && entry.Port != chromecastPort {
		address = net.JoinHostPort(host, strconv.Itoa(entry.Port))
	}

	return device.Info{
		Name:    cmp.Or(entry.DeviceName, entry.Name, entry.Host),
		Type:    Type,
		Address: address,
	}, true
}

// Play returns the receiver's own verdict on the LOAD, so a refused URL fails the hand-off.
func (c *chromecastDevice) Play(ctx context.Context, streamURL *url.URL, contentType string) error {
	transport, err := c.mediaReceiver(ctx)
	if err != nil {
		return fmt.Errorf("starting the chromecast's media receiver: %w", err)
	}
	if err := c.ch.send(transport, nsConnection, &castmedia.PayloadHeader{Type: msgConnect}); err != nil {
		return fmt.Errorf("starting chromecast playback: %w", err)
	}
	c.watchMu.Lock()
	c.watch.begin(streamURL.String())
	c.watchMu.Unlock()
	reply, err := c.ch.request(ctx, transport, nsMedia, &castmedia.LoadMediaCommand{
		PayloadHeader: castmedia.PayloadHeader{Type: msgLoad},
		Media:         castmedia.MediaItem{ContentId: streamURL.String(), ContentType: contentType, StreamType: "BUFFERED"},
		Autoplay:      true,
	})
	if err == nil {
		err = loadVerdict(reply)
	}
	if err != nil {
		c.watchMu.Lock()
		c.watch.disarm()
		c.watchMu.Unlock()
		return fmt.Errorf("starting chromecast playback: %w", err)
	}
	return nil
}

// loadVerdict reads the answer to LOAD: a media status accepts it unless the player already failed.
func loadVerdict(reply []byte) error {
	var response castmedia.MediaStatusResponse
	if err := json.Unmarshal(reply, &response); err != nil {
		return fmt.Errorf("undecodable answer to LOAD: %w", err)
	}
	if response.Type != msgMediaStatus {
		return fmt.Errorf("chromecast refused the media (%s)", response.Type)
	}
	for _, status := range response.Status {
		if status.PlayerState == stateIdle && status.IdleReason == idleError {
			return errors.New("chromecast refused the media: the receiver went idle with an error")
		}
	}
	return nil
}

// mediaReceiver returns the Default Media Receiver's transport, launching it unless it already runs.
func (c *chromecastDevice) mediaReceiver(ctx context.Context) (string, error) {
	status, err := c.receiverStatus(ctx, &castmedia.PayloadHeader{Type: msgGetStatus})
	if err != nil {
		return "", err
	}
	if transport, ok := defaultMediaTransport(status); ok {
		return transport, nil
	}
	status, err = c.receiverStatus(ctx, &castmedia.LaunchRequest{PayloadHeader: castmedia.PayloadHeader{Type: "LAUNCH"}, AppId: defaultMediaReceiver})
	if err != nil {
		return "", err
	}
	if transport, ok := defaultMediaTransport(status); ok {
		return transport, nil
	}
	return "", errors.New("the chromecast answered the launch without the Default Media Receiver running")
}

func (c *chromecastDevice) receiverStatus(ctx context.Context, payload castmedia.Payload) (castmedia.ReceiverStatusResponse, error) {
	var status castmedia.ReceiverStatusResponse
	reply, err := c.ch.request(ctx, receiverID, nsReceiver, payload)
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal(reply, &status); err != nil {
		return status, fmt.Errorf("undecodable receiver status: %w", err)
	}
	if status.Type != "RECEIVER_STATUS" {
		return status, fmt.Errorf("the chromecast answered %s", status.Type)
	}
	return status, nil
}

func defaultMediaTransport(status castmedia.ReceiverStatusResponse) (string, bool) {
	for _, app := range status.Status.Applications {
		if app.AppId == defaultMediaReceiver && app.TransportId != "" {
			return app.TransportId, true
		}
	}
	return "", false
}

// AwaitEnd observes the Cast channel rather than polling it.
func (c *chromecastDevice) AwaitEnd(ctx context.Context) error {
	select {
	case <-c.done:
		return c.playbackErr
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (c *chromecastDevice) watchMessage(payload []byte) {
	var response castmedia.MediaStatusResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return
	}
	c.watchMu.Lock()
	over, playErr := playbackOutcome(&c.watch, &response)
	if over {
		c.finish.Do(func() {
			c.playbackErr = playErr
			close(c.done)
		})
	}
	c.watchMu.Unlock()
}

type chromecastPlayback struct {
	armed   bool
	content string
	session int
	active  bool
}

func (w *chromecastPlayback) begin(content string) {
	w.armed = true
	w.content = content
	w.session = 0
	w.active = false
}

func (w *chromecastPlayback) disarm() {
	w.armed = false
	w.content = ""
	w.session = 0
	w.active = false
}

func (w *chromecastPlayback) observe(status castmedia.Media) (bool, error) {
	if !w.armed {
		return false, nil
	}
	if w.session == 0 {
		if status.Media.ContentId != w.content || status.MediaSessionId == 0 {
			return false, nil
		}
		w.session = status.MediaSessionId
	} else if status.MediaSessionId != w.session {
		return false, nil
	}

	switch status.PlayerState {
	case "BUFFERING", "PLAYING", "PAUSED":
		w.active = true
	case stateIdle:
		switch status.IdleReason {
		case idleError:
			if w.active {
				return true, fmt.Errorf("chromecast playback ended with receiver error")
			}
			return true, fmt.Errorf("chromecast refused the media: the receiver went idle with an error before playback began")
		case "FINISHED", "CANCELLED", "INTERRUPTED":
			return w.active, nil
		}
	}
	return false, nil
}

func playbackOutcome(w *chromecastPlayback, response *castmedia.MediaStatusResponse) (bool, error) {
	if !w.armed {
		return false, nil
	}
	switch response.Type {
	case msgClose:
		return w.active, nil
	case "LOAD_FAILED", "LOAD_CANCELLED":
		return true, fmt.Errorf("chromecast refused the media (%s)", response.Type)
	case msgMediaStatus:
		for _, status := range response.Status {
			if over, err := w.observe(status); over {
				return true, err
			}
		}
		return false, nil
	default:
		return false, nil
	}
}

func (c *chromecastDevice) Close() error {
	return c.ch.Close()
}

var chromecastCapabilities = media.Capabilities{
	Containers:      []string{media.HLS, media.MPEGTS, media.MP4, media.WebM},
	ServedContainer: media.MP4,
	Video: []media.VideoSupport{
		device.VideoSupport(media.CodecH264),
		device.VideoSupport(media.CodecVP8),
	},
	Audio: []media.AudioSupport{
		{Codec: media.CodecAAC, MaxChannels: 6},
		{Codec: media.CodecAC3},
		{Codec: media.CodecEAC3},
		{Codec: media.CodecMP3},
		{Codec: media.CodecVorbis},
	},
}

func (c *chromecastDevice) Capabilities() media.Capabilities { return chromecastCapabilities }

// StreamHeaders returns nil: Chromecast needs no protocol-specific headers.
func (c *chromecastDevice) StreamHeaders(string) map[string]string {
	return nil
}
