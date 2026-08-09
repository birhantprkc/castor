package device

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"

	"github.com/vishen/go-chromecast/application"
	castdns "github.com/vishen/go-chromecast/dns"

	"github.com/stupside/castor/internal/media"
)

const chromecastPort = 8009

type chromecastDevice struct {
	app *application.Application
}

var _ Device = (*chromecastDevice)(nil)

// chromecast is the Google Cast strategy. Cast is a smart client: handed a URL it
// fetches the media itself, so it self-fetches.
type chromecast struct{}

var _ renderer = chromecast{}

func (chromecast) selfFetches() bool { return true }

// connect opens the cast application channel. The underlying library's Start has
// no context support, so cancellation cannot interrupt the dial, and Cast needs no
// family connect settings, so ctx and cfg are ignored. Address may be a bare host
// (default port 8009) or host:port as produced by discovery; cast groups advertise
// on non-default ports.
func (chromecast) connect(_ context.Context, info Info, _ Config) (Device, error) {
	host := info.Address
	port := chromecastPort
	if h, p, err := net.SplitHostPort(info.Address); err == nil {
		if n, err := strconv.Atoi(p); err == nil {
			host, port = h, n
		}
	}

	app := application.NewApplication(
		application.WithCacheDisabled(true),
	)
	if err := app.Start(host, port); err != nil {
		return nil, fmt.Errorf("connecting to chromecast: %w", err)
	}
	return &chromecastDevice{app: app}, nil
}

// locate pins a Chromecast by address, bypassing mDNS discovery. connect already
// accepts a bare host (default port 8009) or host:port, so the address passes
// straight through; a cast group on a non-default port is reached by pinning
// host:port.
func (chromecast) locate(_ context.Context, name, address string) (Info, error) {
	return Info{
		Name:    cmp.Or(name, hostOnly(address)),
		Type:    TypeChromecast,
		Address: address,
	}, nil
}

// discover browses mDNS (_googlecast._tcp) for cast devices until ctx expires.
// The entries channel is closed by the library when ctx is done, so ranging over
// it consumes the full discovery window.
func (chromecast) discover(ctx context.Context) []Info {
	entries, err := castdns.DiscoverCastDNSEntries(ctx, nil)
	if err != nil {
		slog.WarnContext(ctx, "chromecast discovery error", "error", err)
		return nil
	}

	var devices []Info
	seen := make(map[string]struct{})
	for entry := range entries {
		info, ok := chromecastInfo(entry)
		if !ok {
			continue
		}

		// mDNS re-announces the same device; dedupe by UUID, falling back
		// to the resolved address when the entry carries no UUID.
		key := cmp.Or(entry.UUID, info.Address)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		devices = append(devices, info)
	}
	return devices
}

// chromecastInfo maps an mDNS cast entry to a device Info, reporting false when
// the entry advertises no usable IP address. IPv4 is preferred over IPv6, and
// the friendly DeviceName over the mDNS instance and host names. A non-default
// port is preserved as host:port so cast groups, which advertise on random high
// ports rather than 8009, stay connectable; a bare host implies port 8009.
func chromecastInfo(entry castdns.CastEntry) (Info, bool) {
	var host string
	switch {
	case entry.AddrV4 != nil:
		host = entry.AddrV4.String()
	case entry.AddrV6 != nil:
		host = entry.AddrV6.String()
	default:
		return Info{}, false
	}

	address := host
	if entry.Port > 0 && entry.Port != chromecastPort {
		address = net.JoinHostPort(host, strconv.Itoa(entry.Port))
	}

	return Info{
		Name:    cmp.Or(entry.DeviceName, entry.Name, entry.Host),
		Type:    TypeChromecast,
		Address: address,
	}, true
}

func (c *chromecastDevice) Play(_ context.Context, streamURL *url.URL, contentType string) error {
	if err := c.app.Load(streamURL.String(), 0, contentType, false, true, true); err != nil {
		return fmt.Errorf("starting chromecast playback: %w", err)
	}
	return nil
}

func (c *chromecastDevice) Close() error {
	return c.app.Close(false)
}

// chromecastCapabilities is the documented Cast media profile, which is the whole
// of what castor can know here: unlike DLNA there is no runtime query, so the
// envelope is declared rather than negotiated. SelfFetch is true, so an accepted
// container casts straight through with no castor-served stream and anything else
// is remuxed to mp4.
//
// The video envelope is deliberately absent, and absent means unknown rather than
// none. "Chromecast" names a decade of receivers: a first generation decodes no
// HEVC and an Ultra decodes it at half the bitrate of H.264, and nothing here can
// tell them apart, because the only fact available is the family the operator
// typed. Declaring the family's best profile would make castor aim a forced
// re-encode at HEVC for every receiver wearing the name, which black-screens the
// ones that cannot decode it, and a black screen is a worse answer than a larger
// H.264 stream. The honest fix is to ask the device (Cast reports receiver
// capabilities on connect) rather than to assert on its behalf here.
//
// The audio envelope drives the remux path only: Cast decodes AAC, AC-3 and
// E-AC-3, so a 5.1 track in an MKV is stream-copied intact instead of downmixed.
// AAC is capped at 5.1 (6 channels): Cast decodes multichannel AAC only up to 5.1,
// so a 7.1 AAC track is re-encoded (to a Dolby codec, else stereo) rather than
// copied to a receiver that cannot play it.
var chromecastCapabilities = media.Renderer{
	SelfFetch:       chromecast{}.selfFetches(),
	Containers:      []string{media.HLS, media.MP4, media.MKV, media.WebM},
	ServedContainer: media.MP4,
	Audio: []media.AudioSupport{
		{Codec: media.CodecAAC, MaxChannels: 6},
		{Codec: media.CodecAC3},
		{Codec: media.CodecEAC3},
	},
}

// Capabilities reports Chromecast's known receiver profile. There is no runtime
// query as there is for DLNA, so this is the documented Cast media support,
// resolved from the device itself for interface parity with the DLNA path.
func (c *chromecastDevice) Capabilities() media.Renderer { return chromecastCapabilities }

// StreamHeaders returns nil: Chromecast needs no protocol-specific headers on
// the local stream server's responses.
func (c *chromecastDevice) StreamHeaders(string) map[string]string {
	return nil
}
