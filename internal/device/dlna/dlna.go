// Package dlna: UPnP MediaRenderer discovery and playback control.
package dlna

import (
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/huin/goupnp"
	"github.com/huin/goupnp/soap"

	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

// capsTimeout: ConnectionManager timeout; degrades rather than stall.
const capsTimeout = 3 * time.Second

// actionTimeout: AVTransport action timeout (prevents wedging on unresponsive renderers).
const actionTimeout = 10 * time.Second

const (
	dlnaSearchTarget = "urn:schemas-upnp-org:device:MediaRenderer:1"
	ssdpPort         = "1900"
	// msearchTimeout bounds the unicast M-SEARCH when the caller sets no deadline.
	msearchTimeout = 3 * time.Second
)

// serviceVersions: UPnP service versions (newest first); many implement :3, not just :1.
var serviceVersions = []int{3, 2, 1}

// dlnaDevice: AVTransport renderer with generic client (avoid goupnp av1 :1 URN hardcoding).
type dlnaDevice struct {
	transport goupnp.ServiceClient
	caps      media.Capabilities
}

func (d *dlnaDevice) Capabilities() media.Capabilities { return d.caps }

// name: renderer name (FriendlyName, location, or fallback).
func (d *dlnaDevice) name() string {
	var friendly, location string
	if d.transport.RootDevice != nil {
		friendly = d.transport.RootDevice.Device.FriendlyName
	}
	if d.transport.Location != nil {
		location = d.transport.Location.String()
	}
	return cmp.Or(friendly, location, "DLNA renderer")
}

var _ device.Device = (*dlnaDevice)(nil)

// Family: UPnP AVTransport strategy (renderers don't self-fetch).
type Family struct{}

// Type is the name config.yaml selects this family by.
const Type device.Type = "dlna"

func (Family) Type() device.Type { return Type }

var _ device.Family = Family{}

func (Family) SelfFetches() bool { return false }

func (Family) Discover(ctx context.Context) []device.Info {
	results, err := goupnp.DiscoverDevicesCtx(ctx, dlnaSearchTarget)
	if err != nil {
		slog.WarnContext(ctx, "dlna discovery error", "error", err)
		return nil
	}

	var devices []device.Info
	seen := make(map[string]struct{})
	for _, result := range results {
		info, ok := dlnaInfo(result)
		if !ok {
			continue
		}

		// SSDP re-announces the same device; dedupe by USN, falling back to the address.
		key := cmp.Or(result.USN, info.Address)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		devices = append(devices, info)
	}
	return devices
}

func dlnaInfo(result goupnp.MaybeRootDevice) (device.Info, bool) {
	if result.Root == nil || result.Location == nil {
		return device.Info{}, false
	}

	return device.Info{
		Name:    result.Root.Device.FriendlyName,
		Type:    Type,
		Address: result.Location.String(),
	}, true
}

// Locate: HTTP address trusted; others use unicast M-SEARCH (no multicast).
func (Family) Locate(ctx context.Context, address string) (string, error) {
	if u, err := url.Parse(address); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return address, nil
	}

	location, err := searchDLNADescription(ctx, address)
	if err != nil {
		return "", fmt.Errorf(
			"resolving DLNA description for %q (device did not answer a unicast SSDP search; "+
				"set device.host to its full description URL instead): %w", address, err)
	}
	return location, nil
}

// searchDLNADescription: unicast M-SEARCH for description (no interface enumeration).
func searchDLNADescription(ctx context.Context, host string) (string, error) {
	target := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		target = net.JoinHostPort(host, ssdpPort)
	}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp4", target)
	if err != nil {
		return "", fmt.Errorf("dialing %s: %w", target, err)
	}
	defer conn.Close()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(msearchTimeout)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return "", err
	}

	msearch := strings.Join([]string{
		"M-SEARCH * HTTP/1.1",
		"HOST: " + target,
		`MAN: "ssdp:discover"`,
		"MX: 1",
		"ST: " + dlnaSearchTarget,
		"", "",
	}, "\r\n")
	if _, err := conn.Write([]byte(msearch)); err != nil {
		return "", fmt.Errorf("sending M-SEARCH: %w", err)
	}

	// A device answers once per matching root/embedded device.
	buf := make([]byte, 2048)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return "", fmt.Errorf("awaiting SSDP response: %w", err)
		}
		if location := parseSSDPLocation(buf[:n]); location != "" {
			return location, nil
		}
	}
}

// parseSSDPLocation matches case-insensitively, per RFC 2616, and returns "" when absent.
func parseSSDPLocation(response []byte) string {
	for line := range strings.SplitSeq(string(response), "\r\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "LOCATION") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// Connect negotiates capabilities at bind time.
func (Family) Connect(ctx context.Context, info device.Info) (device.Device, error) {
	u, err := url.Parse(info.Address)
	if err != nil {
		return nil, fmt.Errorf("parsing device location URL: %w", err)
	}

	loc, err := goupnp.DeviceByURLCtx(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("fetching device description: %w", err)
	}

	transport, err := findService(loc, u, "AVTransport")
	if err != nil {
		return nil, fmt.Errorf("creating AVTransport client: %w", err)
	}
	return &dlnaDevice{transport: transport, caps: negotiateCaps(ctx, loc, u)}, nil
}

// findService returns a client for the newest version of the named service the device publishes.
func findService(root *goupnp.RootDevice, loc *url.URL, service string) (goupnp.ServiceClient, error) {
	for _, version := range serviceVersions {
		urn := fmt.Sprintf("urn:schemas-upnp-org:service:%s:%d", service, version)
		clients, err := goupnp.NewServiceClientsFromRootDevice(root, loc, urn)
		if err != nil || len(clients) == 0 {
			continue
		}
		return clients[0], nil
	}
	return goupnp.ServiceClient{}, fmt.Errorf(
		"no %s service (v1-v3) found on device %q (UDN=%q)",
		service, root.Device.FriendlyName, root.Device.UDN)
}

// negotiateCaps asks the renderer what it accepts over ConnectionManager GetProtocolInfo.
func negotiateCaps(ctx context.Context, loc *goupnp.RootDevice, u *url.URL) media.Capabilities {
	manager, err := findService(loc, u, "ConnectionManager")
	if err != nil {
		slog.WarnContext(ctx, "no ConnectionManager service; using conservative capabilities", "error", err)
		return fallbackCaps()
	}
	ctx, cancel := context.WithTimeout(ctx, capsTimeout)
	defer cancel()

	response := &struct{ Source, Sink string }{}
	if err := manager.SOAPClient.PerformActionCtx(
		ctx, manager.Service.ServiceType, "GetProtocolInfo", nil, response); err != nil {
		slog.WarnContext(ctx, "GetProtocolInfo failed; using conservative capabilities", "error", err)
		return fallbackCaps()
	}
	sink := response.Sink
	caps := parseSinkProtocolInfo(sink)
	if len(caps.Video) == 0 {
		slog.WarnContext(ctx, "renderer advertised no known video codec; using conservative capabilities")
		return fallbackCaps()
	}
	// A nil Audio is not a renderer that plays silence, it is one that said nothing about audio.
	if len(caps.Audio) == 0 {
		slog.WarnContext(ctx, "renderer advertised no known audio codec; assuming the conservative audio floor",
			"codecs", codecNames(caps.Video))
		caps.Audio = fallbackCaps().Audio
	}
	slog.InfoContext(ctx, "negotiated renderer capabilities", "codecs", codecNames(caps.Video), "containers", caps.Containers)
	return caps
}

// audioSupportFor builds the copy envelope for an audio codec.
func audioSupportFor(codec media.Codec) media.AudioSupport {
	if codec == media.CodecAAC {
		return media.AudioSupport{Codec: codec, MaxChannels: 2}
	}
	return media.AudioSupport{Codec: codec}
}

// The fixed order capabilities are reported in, so a given Sink always yields the same record.
var (
	discoverableCodecs      = []media.Codec{media.CodecH264, media.CodecHEVC}
	discoverableAudioCodecs = []media.Codec{media.CodecAAC, media.CodecAC3, media.CodecEAC3}
)

func fallbackCaps() media.Capabilities {
	return media.Capabilities{
		Containers:      []string{media.MPEGTS},
		Video:           []media.VideoSupport{device.VideoSupport(media.CodecH264)},
		Audio:           []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
		ServedContainer: servedContainer,
		Deinterlaces:    true,
	}
}

// servedContainer is what castor muxes for a DLNA media.
const servedContainer = media.MPEGTS

// parseSinkProtocolInfo maps a ConnectionManager Sink protocolInfo CSV into capabilities.
func parseSinkProtocolInfo(sink string) media.Capabilities {
	present := map[media.Codec]bool{}
	audioPresent := map[media.Codec]bool{}
	containers := map[string]bool{}
	for entry := range strings.SplitSeq(sink, ",") {
		fields := strings.SplitN(strings.TrimSpace(entry), ":", 4)
		if len(fields) < 3 || !strings.EqualFold(fields[0], "http-get") {
			continue
		}
		mime := strings.ToLower(fields[2])
		info := ""
		if len(fields) == 4 {
			info = strings.ToUpper(fields[3])
		}
		if c, ok := codecFromProfile(mime, info); ok {
			present[c] = true
		}
		if c, ok := audioFromProfile(mime, info); ok {
			audioPresent[c] = true
		}
		if ct, ok := containerFromMIME(mime); ok {
			containers[ct] = true
		}
	}

	// A television deinterlaces: broadcast reaches it as fields.
	r := media.Capabilities{ServedContainer: servedContainer, Deinterlaces: true}
	for _, c := range discoverableCodecs {
		if present[c] {
			r.Video = append(r.Video, device.VideoSupport(c))
		}
	}
	for _, c := range discoverableAudioCodecs {
		if audioPresent[c] {
			r.Audio = append(r.Audio, audioSupportFor(c))
		}
	}
	for _, ct := range []string{media.MPEGTS, media.MP4} {
		if containers[ct] {
			r.Containers = append(r.Containers, ct)
		}
	}
	return r
}

// codecFromProfile reads the DLNA.ORG_PN token (already upper-cased), failing that the MIME type.
func codecFromProfile(mime, pn string) (media.Codec, bool) {
	switch {
	case strings.Contains(pn, "HEVC") || strings.Contains(pn, "H265") || strings.Contains(mime, "hevc") || strings.Contains(mime, "h265"):
		return media.CodecHEVC, true
	case strings.Contains(pn, "AVC") || strings.Contains(pn, "H264") || strings.Contains(mime, "avc") || strings.Contains(mime, "h264"):
		return media.CodecH264, true
	}
	return "", false
}

func audioFromProfile(mime, pn string) (media.Codec, bool) {
	switch {
	case strings.Contains(pn, "EAC3") || strings.Contains(mime, "eac3") || strings.Contains(mime, "dd+"):
		return media.CodecEAC3, true
	case strings.Contains(pn, "AC3") || strings.Contains(mime, "ac3") || strings.Contains(mime, "dolby.dd"):
		return media.CodecAC3, true
	case strings.Contains(pn, "AAC") || strings.Contains(mime, "aac") || mime == "audio/mp4":
		return media.CodecAAC, true
	}
	return "", false
}

// containerFromMIME normalises DLNA MIME spellings to the content types the pipeline speaks.
func containerFromMIME(mime string) (string, bool) {
	switch mime {
	case media.MPEGTS, "video/mpeg", "video/vnd.dlna.mpeg-tts", "video/x-mpegts":
		return media.MPEGTS, true
	case media.MP4:
		return media.MP4, true
	}
	return "", false
}

func codecNames(vs []media.VideoSupport) []string {
	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = string(v.Codec)
	}
	return names
}

const (
	transportLockedRetries = 5
	transportLockedDelay   = 300 * time.Millisecond
)

// Play hands over one video resource with no caption track: subtitles are burned in upstream.
func (d *dlnaDevice) Play(ctx context.Context, streamURL *url.URL, contentType string) error {
	metadata, err := buildDIDLMetadata(streamURL, contentType)
	if err != nil {
		return fmt.Errorf("building DIDL-Lite metadata: %w", err)
	}
	slog.DebugContext(ctx, "DIDL metadata", "xml", metadata)

	setURI := &struct {
		InstanceID         string
		CurrentURI         string
		CurrentURIMetaData string
	}{"0", streamURL.String(), metadata}
	if err := retryTransportLocked(ctx, transportLockedRetries, transportLockedDelay, func() error {
		return d.action(ctx, "SetAVTransportURI", setURI)
	}); err != nil {
		return fmt.Errorf("setting transport URI: %w", err)
	}

	play := &struct {
		InstanceID string
		Speed      string
	}{"0", "1"}
	if err := retryTransportLocked(ctx, transportLockedRetries, transportLockedDelay, func() error {
		return d.action(ctx, "Play", play)
	}); err != nil {
		return fmt.Errorf("starting playback: %w", err)
	}
	return nil
}

func retryTransportLocked(ctx context.Context, retries int, delay time.Duration, do func() error) error {
	var err error
	for attempt := range retries {
		err = do()
		if !isTransportLocked(err) {
			return err
		}
		slog.DebugContext(ctx, "transport locked, retrying action", "attempt", attempt+1)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
	}
	return err
}

func isTransportLocked(err error) bool {
	fault, ok := errors.AsType[*soap.SOAPFaultError](err)
	return ok && fault.Detail.UPnPError.Errorcode == 705
}

// action performs a SOAP action namespaced to the service version the device published.
func (d *dlnaDevice) action(ctx context.Context, name string, request any) error {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	return d.transport.SOAPClient.PerformActionCtx(
		ctx, d.transport.Service.ServiceType, name, request, nil)
}

const transportQuery = "GetTransportInfo"

// transportWatch holds the one thing that ends a cast from the renderer's side.
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
func (d *dlnaDevice) AwaitEnd(ctx context.Context) error {
	return awaitTransportEnd(ctx, d.name(), device.UnreachableWindow, device.PollInterval, d.transportState)
}

// awaitTransportEnd folds the AVTransport state machine over the shared poll loop.
func awaitTransportEnd(ctx context.Context, name string, unreachableFor, interval time.Duration, poll func(context.Context) (string, error)) error {
	var watch transportWatch
	return device.AwaitPolledEnd(ctx, name, transportQuery, unreachableFor, interval,
		func(ctx context.Context) (bool, error) {
			state, err := poll(ctx)
			if err != nil {
				return false, err
			}
			return watch.observe(state), nil
		})
}

func (d *dlnaDevice) transportState(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, device.PollTimeout)
	defer cancel()
	var response struct {
		CurrentTransportState  string
		CurrentTransportStatus string
		CurrentSpeed           string
	}
	err := d.transport.SOAPClient.PerformActionCtx(ctx, d.transport.Service.ServiceType,
		"GetTransportInfo", &struct{ InstanceID string }{"0"}, &response)
	if err != nil {
		return "", err
	}
	return response.CurrentTransportState, nil
}

func (d *dlnaDevice) Close() error {
	return nil
}

// StreamHeaders returns the headers a DLNA renderer expects on a stream response.
func (d *dlnaDevice) StreamHeaders(contentType string) map[string]string {
	return map[string]string{
		"Connection":               "close",
		"Accept-Ranges":            "none",
		"transferMode.dlna.org":    "Streaming",
		"contentFeatures.dlna.org": contentFeatures(contentType),
	}
}

// DLNA.ORG_FLAGS advertised for a live stream and for a whole file.
const (
	dlnaFlagsLive = "8D300000000000000000000000000000"
	dlnaFlagsFile = "01300000000000000000000000000000"
)

// dlnaProfileFor returns the DLNA PN and FLAGS for a content type.
func dlnaProfileFor(contentType string) (name, flags string) {
	switch contentType {
	case media.MPEGTS:
		return "MPEG_TS_HD_NA_ISO", dlnaFlagsLive
	case media.MP4:
		return "AVC_MP4_HP_HD_AAC", dlnaFlagsFile
	}
	return "", dlnaFlagsLive
}

func contentFeatures(contentType string) string {
	name, flags := dlnaProfileFor(contentType)
	return fmt.Sprintf("DLNA.ORG_PN=%s;DLNA.ORG_OP=00;DLNA.ORG_CI=1;DLNA.ORG_FLAGS=%s", name, flags)
}

type didlLite struct {
	XMLName xml.Name `xml:"DIDL-Lite"`
	XMLNS   string   `xml:"xmlns,attr"`
	DC      string   `xml:"xmlns:dc,attr"`
	UPnP    string   `xml:"xmlns:upnp,attr"`
	Item    didlItem `xml:"item"`
}

type didlItem struct {
	ID         string  `xml:"id,attr"`
	ParentID   string  `xml:"parentID,attr"`
	Restricted string  `xml:"restricted,attr"`
	Title      string  `xml:"dc:title"`
	Class      string  `xml:"upnp:class"`
	Res        didlRes `xml:"res"`
}

type didlRes struct {
	ProtocolInfo string `xml:"protocolInfo,attr"`
	Value        string `xml:",chardata"`
}

// buildDIDLMetadata returns the DIDL-Lite XML the renderer needs to play streamURL.
func buildDIDLMetadata(streamURL *url.URL, contentType string) (string, error) {
	item := didlLite{
		XMLNS: "urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/",
		DC:    "http://purl.org/dc/elements/1.1/",
		UPnP:  "urn:schemas-upnp-org:metadata-1-0/upnp/",
		Item: didlItem{
			ID:         "0",
			ParentID:   "-1",
			Restricted: "1",
			Title:      "Castor Stream",
			Class:      "object.item.videoItem",
			Res: didlRes{
				ProtocolInfo: fmt.Sprintf("http-get:*:%s:%s", contentType, contentFeatures(contentType)),
				Value:        streamURL.String(),
			},
		},
	}

	data, err := xml.Marshal(item)
	if err != nil {
		return "", fmt.Errorf("marshaling DIDL-Lite: %w", err)
	}
	return string(data), nil
}
