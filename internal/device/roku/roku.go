package roku

import (
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/huin/goupnp/httpu"
	"github.com/huin/goupnp/ssdp"

	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

const (
	rokuSearchTarget   = "roku:ecp"
	rokuDiscoverySends = 3
	rokuDefaultAppID   = "dev"
	rokuDefaultDevUser = "rokudev"
	rokuHTTPTimeout    = 10 * time.Second
	rokuECPPort        = "8060"
)

type Config struct {
	AppID    string `yaml:"app_id"`
	Password string `yaml:"password"`
}

type rokuDevice struct {
	ecp   *url.URL // http://<ip>:8060
	appID string
	name  string
	hc    *http.Client
}

var _ device.Device = (*rokuDevice)(nil)

// Family is the Roku ECP strategy, built with the operator's Roku settings.
type Family struct {
	Config Config
}

// Type is the name config.yaml selects this family by.
const Type device.Type = "roku"

func (Family) Type() device.Type { return Type }

var _ device.Family = Family{}

func (Family) SelfFetches() bool { return true }

// discover finds Rokus over SSDP.
func (Family) Discover(ctx context.Context) []device.Info {
	hc, err := httpu.NewHTTPUClient()
	if err != nil {
		slog.WarnContext(ctx, "roku discovery", "error", err)
		return nil
	}
	defer hc.Close()

	responses, err := ssdp.RawSearch(ctx, hc, rokuSearchTarget, rokuDiscoverySends)
	if err != nil {
		slog.WarnContext(ctx, "roku discovery", "error", err)
		return nil
	}

	var locations []*url.URL
	seen := make(map[string]struct{})
	for _, resp := range responses {
		loc, err := resp.Location()
		if err != nil {
			continue
		}
		key := cmp.Or(resp.Header.Get("USN"), loc.String())
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		locations = append(locations, loc)
	}

	// ONE window for every name, and the lookups run over it together.
	nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rokuHTTPTimeout)
	defer cancel()

	client := &http.Client{Timeout: rokuHTTPTimeout}
	names := make([]string, len(locations))
	var wg sync.WaitGroup
	for i, loc := range locations {
		wg.Go(func() { names[i] = rokuName(nctx, client, loc) })
	}
	wg.Wait()

	var devices []device.Info
	for i, loc := range locations {
		if info, ok := rokuInfo(loc.String(), names[i]); ok {
			devices = append(devices, info)
		}
	}
	return devices
}

// Locate reduces a pinned address to its ECP root, bypassing SSDP discovery.
func (Family) Locate(_ context.Context, address string) (string, error) {
	host := address
	if u, err := url.Parse(address); err == nil && u.Host != "" {
		host = u.Host
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, rokuECPPort)
	}
	return (&url.URL{Scheme: "http", Host: host}).String(), nil
}

func rokuInfo(location, name string) (device.Info, bool) {
	u, err := url.Parse(location)
	if err != nil || u.Host == "" {
		return device.Info{}, false
	}
	return device.Info{
		Name:    cmp.Or(name, u.Hostname()),
		Type:    Type,
		Address: location,
	}, true
}

// rokuName reads the owner-set name from /query/device-info, falling back to the host on any failure.
func rokuName(ctx context.Context, hc *http.Client, ecpRoot *url.URL) string {
	u := *ecpRoot
	u.Path = "/query/device-info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ecpRoot.Hostname()
	}
	resp, err := hc.Do(req)
	if err != nil {
		return ecpRoot.Hostname()
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return ecpRoot.Hostname()
	}
	return cmp.Or(parseDeviceInfoName(body), ecpRoot.Hostname())
}

func parseDeviceInfoName(body []byte) string {
	var info struct {
		UserDeviceName string `xml:"user-device-name"`
		ModelName      string `xml:"model-name"`
	}
	if err := xml.Unmarshal(body, &info); err != nil {
		return ""
	}
	return cmp.Or(strings.TrimSpace(info.UserDeviceName), strings.TrimSpace(info.ModelName))
}

func (f Family) Connect(ctx context.Context, info device.Info) (device.Device, error) {
	rc := f.Config

	root, err := url.Parse(info.Address)
	if err != nil || root.Host == "" {
		return nil, fmt.Errorf("parsing roku address %q: %w", info.Address, err)
	}

	dev := &rokuDevice{
		ecp:   &url.URL{Scheme: "http", Host: root.Host},
		appID: cmp.Or(rc.AppID, rokuDefaultAppID),
		name:  info.Name,
		hc:    &http.Client{Timeout: rokuHTTPTimeout},
	}

	if dev.appID == rokuDefaultAppID {
		if err := dev.ensureChannel(ctx, rc); err != nil {
			return nil, err
		}
		return dev, nil
	}

	apps, err := dev.queryApps(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying roku apps: %w", err)
	}
	if !appInstalled(apps, dev.appID) {
		return nil, fmt.Errorf("roku channel %q is not installed on %q", dev.appID, dev.name)
	}
	return dev, nil
}

// ensureChannel guarantees Castor's own dev channel occupies the sideload slot.
func (r *rokuDevice) ensureChannel(ctx context.Context, cfg Config) error {
	apps, err := r.queryApps(ctx)
	if err != nil {
		return fmt.Errorf("querying roku apps: %w", err)
	}
	if devChannelInstalled(apps) {
		return nil
	}
	if cfg.Password == "" {
		if appInstalled(apps, rokuDefaultAppID) {
			return fmt.Errorf("a different sideloaded channel occupies the Roku dev slot; set device.roku.password so Castor can replace it")
		}
		return fmt.Errorf("roku channel not installed and no developer password set: enable Developer Mode on the Roku, set a web-server password, and put it in device.roku.password")
	}
	slog.InfoContext(ctx, "sideloading roku channel", "host", r.ecp.Hostname())
	return r.sideloadChannel(ctx, rokuDefaultDevUser, cfg.Password)
}

func (r *rokuDevice) queryApps(ctx context.Context) ([]byte, error) {
	u := *r.ecp
	u.Path = "/query/apps"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query/apps: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

type rokuApp struct {
	ID    string `xml:"id,attr"`
	Title string `xml:",chardata"`
}

func parseApps(body []byte) []rokuApp {
	var list struct {
		Apps []rokuApp `xml:"app"`
	}
	if err := xml.Unmarshal(body, &list); err != nil {
		return nil
	}
	return list.Apps
}

func devChannelInstalled(body []byte) bool {
	for _, a := range parseApps(body) {
		if a.ID == rokuDefaultAppID && strings.TrimSpace(a.Title) == rokuChannelTitle {
			return true
		}
	}
	return false
}

func appInstalled(body []byte, id string) bool {
	for _, a := range parseApps(body) {
		if a.ID == id {
			return true
		}
	}
	return false
}

func (r *rokuDevice) Play(ctx context.Context, streamURL *url.URL, contentType string) error {
	q := url.Values{}
	q.Set(rokuChannelParamURL, streamURL.String())
	q.Set(rokuChannelParamFormat, streamFormatFor(contentType))

	u := *r.ecp
	u.Path = "/launch/" + r.appID
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return fmt.Errorf("launching roku channel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("roku launch: %s (channel %q installed?)", resp.Status, r.appID)
	}
	return nil
}

const rokuMediaPlayerQuery = "/query/media-player"

// AwaitEnd answers exactly ONE of the two things an ECP poll could establish.
func (r *rokuDevice) AwaitEnd(ctx context.Context) error {
	return device.AwaitPolledEnd(ctx, r.name, rokuMediaPlayerQuery, device.UnreachablePolls, device.PollInterval, r.mediaPlayerAnswered)
}

// mediaPlayerAnswered reports only that somebody answered, never that playback is over (see AwaitEnd).
func (r *rokuDevice) mediaPlayerAnswered(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, device.PollTimeout)
	defer cancel()

	u := *r.ecp
	u.Path = rokuMediaPlayerQuery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false, err
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return false, nil
}

func streamFormatFor(contentType string) string {
	switch contentType {
	case media.MP4:
		return "mp4"
	case media.MKV:
		return "mkv"
	default:
		return "hls"
	}
}

// rokuCapabilities describes what Castor's channel plays.
var rokuCapabilities = media.Capabilities{
	Containers:      []string{media.HLS, media.MP4, media.MKV},
	ServedContainer: media.HLS,
	Video:           []media.VideoSupport{device.VideoSupport(media.CodecH264)},
	Audio: []media.AudioSupport{
		{Codec: media.CodecAAC, MaxChannels: 6},
		{Codec: media.CodecAC3},
		{Codec: media.CodecEAC3},
	},
}

func (r *rokuDevice) Capabilities() media.Capabilities       { return rokuCapabilities }
func (r *rokuDevice) StreamHeaders(string) map[string]string { return nil }
func (r *rokuDevice) Close() error                           { return nil }
