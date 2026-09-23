package chromecast

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/vishen/go-chromecast/application"
	castmedia "github.com/vishen/go-chromecast/cast"
	pb "github.com/vishen/go-chromecast/cast/proto"
	castdns "github.com/vishen/go-chromecast/dns"

	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

const chromecastPort = 8009

type chromecastDevice struct {
	app *application.Application

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

// Connect ignores ctx: the library's Start has no context support, so cancellation cannot interrupt the dial.
func (Family) Connect(_ context.Context, info device.Info) (device.Device, error) {
	host, port := chromecastEndpoint(info.Address)

	app := application.NewApplication(
		application.WithCacheDisabled(true),
	)
	if err := app.Start(host, port); err != nil {
		return nil, fmt.Errorf("connecting to chromecast: %w", err)
	}
	dev := &chromecastDevice{app: app, done: make(chan struct{})}
	app.AddMessageFunc(dev.watchMessage)
	return dev, nil
}

// chromecastEndpoint splits a bare host or host:port into the pair the library dials.
func chromecastEndpoint(address string) (string, int) {
	host, port := address, chromecastPort
	if h, p, err := net.SplitHostPort(address); err == nil {
		if n, err := strconv.Atoi(p); err == nil {
			host, port = h, n
		}
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return host, port
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

func (c *chromecastDevice) Play(_ context.Context, streamURL *url.URL, contentType string) error {
	c.watchMu.Lock()
	c.watch.begin(streamURL.String())
	c.watchMu.Unlock()
	if err := c.app.Load(streamURL.String(), 0, contentType, false, true, true); err != nil {
		c.watchMu.Lock()
		c.watch.disarm()
		c.watchMu.Unlock()
		return fmt.Errorf("starting chromecast playback: %w", err)
	}
	return nil
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

func (c *chromecastDevice) watchMessage(msg *pb.CastMessage) {
	if msg.GetPayloadUtf8() == "" {
		return
	}
	var response castmedia.MediaStatusResponse
	if err := json.Unmarshal([]byte(msg.GetPayloadUtf8()), &response); err != nil {
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
	case "IDLE":
		switch status.IdleReason {
		case "ERROR":
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
	case "CLOSE":
		return w.active, nil
	case "LOAD_FAILED", "LOAD_CANCELLED":
		return true, fmt.Errorf("chromecast refused the media (%s)", response.Type)
	case "MEDIA_STATUS":
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
	return c.app.Close(false)
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
