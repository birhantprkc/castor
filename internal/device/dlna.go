package device

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/huin/goupnp"
	"github.com/huin/goupnp/soap"

	"github.com/stupside/castor/internal/media"
)

// capsTimeout bounds the GetProtocolInfo round-trip: a slow or unresponsive
// ConnectionManager degrades to conservative capabilities instead of stalling
// the cast, since this negotiation now gates the copy-vs-encode decision.
const capsTimeout = 3 * time.Second

const (
	// dlnaSearchTarget is the SSDP search target for UPnP MediaRenderers, shared by
	// multicast discovery and the unicast M-SEARCH the direct-connect path uses.
	dlnaSearchTarget = "urn:schemas-upnp-org:device:MediaRenderer:1"
	// ssdpPort is the fixed UDP port an SSDP endpoint listens on.
	ssdpPort = "1900"
	// msearchTimeout bounds the unicast M-SEARCH when the caller sets no deadline.
	msearchTimeout = 3 * time.Second
)

// serviceVersions are the UPnP service versions castor looks for, newest
// first. Renderers publish whichever version their firmware implements (Philips
// and Sony sets commonly advertise :3) and service lookup is an exact URN
// match, so asking only for :1 misses them. UPnP requires a higher service
// version to stay backward compatible with the actions of lower ones, and
// castor calls only SetAVTransportURI, Play and GetProtocolInfo: all unchanged
// since v1.
var serviceVersions = []int{3, 2, 1}

// dlnaDevice is a connected UPnP AVTransport renderer. caps is negotiated once
// at connect (see negotiateCaps) and reported verbatim thereafter.
//
// transport is the generic service client rather than a version-specific
// wrapper: the SOAP action namespace must match the service version the device
// published, and goupnp's av1 clients hardcode the :1 URN, which a :3 service
// can reject as an invalid action.
type dlnaDevice struct {
	transport goupnp.ServiceClient
	caps      media.Renderer
}

func (d *dlnaDevice) Capabilities() media.Renderer { return d.caps }

var _ Device = (*dlnaDevice)(nil)

// dlna is the DLNA/UPnP AVTransport strategy. A DLNA renderer only plays bytes
// castor serves it (it never fetches a source URL), so it does not self-fetch.
type dlna struct{}

var _ renderer = dlna{}

func (dlna) selfFetches() bool { return false }

// discover browses SSDP for UPnP MediaRenderer devices until ctx expires.
func (dlna) discover(ctx context.Context) []Info {
	results, err := goupnp.DiscoverDevicesCtx(ctx, dlnaSearchTarget)
	if err != nil {
		slog.WarnContext(ctx, "dlna discovery error", "error", err)
		return nil
	}

	var devices []Info
	seen := make(map[string]struct{})
	for _, result := range results {
		info, ok := dlnaInfo(result)
		if !ok {
			continue
		}

		// SSDP re-announces the same device; dedupe by USN, falling back
		// to the resolved address when the announcement carries no USN.
		key := cmp.Or(result.USN, info.Address)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		devices = append(devices, info)
	}
	return devices
}

// dlnaInfo maps a discovered UPnP root device to a device Info, reporting false
// when the announcement carries no reachable device to name.
func dlnaInfo(result goupnp.MaybeRootDevice) (Info, bool) {
	if result.Root == nil || result.Location == nil {
		return Info{}, false
	}

	return Info{
		Name:    result.Root.Device.FriendlyName,
		Type:    TypeDLNA,
		Address: result.Location.String(),
	}, true
}

// locate resolves a pinned DLNA target into an Info carrying the device description
// URL connect needs. When address is already that URL (has an http scheme) it is
// trusted verbatim; otherwise address is a bare host or host:port and a unicast SSDP
// M-SEARCH asks the device for its description location. That M-SEARCH is a single
// unicast datagram (see searchDLNADescription), so unlike goupnp's multicast
// discovery it enumerates no interfaces and works where that is denied
// (Android/Termux) or where multicast does not reach the device.
func (dlna) locate(ctx context.Context, name, address string) (Info, error) {
	if u, err := url.Parse(address); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return Info{Name: cmp.Or(name, u.Hostname()), Type: TypeDLNA, Address: address}, nil
	}

	location, err := searchDLNADescription(ctx, address)
	if err != nil {
		return Info{}, fmt.Errorf(
			"resolving DLNA description for %q (device did not answer a unicast SSDP search; "+
				"set device.host to its full description URL instead): %w", address, err)
	}
	return Info{Name: cmp.Or(name, hostOnly(address)), Type: TypeDLNA, Address: location}, nil
}

// searchDLNADescription sends a unicast SSDP M-SEARCH to host and returns the
// LOCATION (the UPnP device description URL) from the first response that carries
// one. host may include a port; the SSDP default (1900) is assumed otherwise. It
// dials a connected UDP socket, which does route selection via connect(2) only and
// enumerates no interfaces, so it avoids the netlink call that fails on Termux.
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

	// A device answers once per matching root/embedded device; read datagrams
	// until one carries a LOCATION or the deadline passes.
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

// parseSSDPLocation extracts the LOCATION header value from a raw SSDP response,
// returning "" when absent. Header names are case-insensitive per RFC 2616.
func parseSSDPLocation(response []byte) string {
	for line := range strings.SplitSeq(string(response), "\r\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "LOCATION") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// connect fetches the device description at info.Address, binds the AVTransport
// client, and negotiates capabilities. DLNA needs no family connect settings, so
// cfg is ignored.
func (dlna) connect(ctx context.Context, info Info, _ Config) (Device, error) {
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

// findService returns a client for the newest version of the named service the
// device publishes, reporting an error when it exposes none castor can drive.
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

// negotiateCaps asks the renderer what it accepts, over ConnectionManager
// GetProtocolInfo, and maps its advertised Sink into a media.Renderer. It is
// best-effort: any failure, or a renderer that advertises no codec we know,
// degrades to fallbackCaps so playback still works (just conservatively).
func negotiateCaps(ctx context.Context, loc *goupnp.RootDevice, u *url.URL) media.Renderer {
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
	slog.InfoContext(ctx, "negotiated renderer capabilities", "codecs", codecNames(caps.Video), "containers", caps.Containers)
	return caps
}

// codecEnvelope is the codec-fixed part of a stream-copy envelope: the profiles
// and bit depths that black-screen or mis-tone a renderer that can't handle them
// (10-bit H.264 is the rare High 10 profile; HDR needs a TV that engages it).
// Adding a codec the pipeline can encode to is one entry here.
type codecEnvelope struct {
	profiles  []string
	bitDepths []int // nil == 8-bit only
}

var codecEnvelopes = map[media.Codec]codecEnvelope{
	media.CodecH264: {profiles: []string{"Constrained Baseline", "Baseline", "Main", "High"}},
	media.CodecHEVC: {profiles: []string{"Main", "Main 10"}, bitDepths: []int{8, 10}},
}

// videoSupportFor builds the copy envelope for a codec: its decode-safety
// profile and bit-depth constraints.
func videoSupportFor(codec media.Codec) media.VideoSupport {
	env := codecEnvelopes[codec]
	return media.VideoSupport{
		Codec:     codec,
		Profiles:  env.profiles,
		BitDepths: env.bitDepths,
	}
}

// audioSupportFor builds the copy envelope for an audio codec. The Sink carries
// no channel count, so AAC (which a renderer commonly decodes stereo-only, its
// multichannel form being a separate profile most sets don't list) is capped at
// stereo: an advertised AAC is trusted to copy a stereo track but a 5.1 AAC
// source re-encodes to a Dolby codec instead. AC-3/E-AC-3 are inherently
// surround, so their advertised support is trusted at full channel count.
func audioSupportFor(codec media.Codec) media.AudioSupport {
	if codec == media.CodecAAC {
		return media.AudioSupport{Codec: codec, MaxChannels: 2}
	}
	return media.AudioSupport{Codec: codec}
}

// discoverableCodecs is the fixed order capabilities are reported in, so a given
// Sink always yields the same Renderer (and the same is testable).
var (
	discoverableCodecs      = []media.Codec{media.CodecH264, media.CodecHEVC}
	discoverableAudioCodecs = []media.Codec{media.CodecAAC, media.CodecAC3, media.CodecEAC3}
)

// fallbackCaps is the conservative envelope used when negotiation yields nothing
// usable: H.264 in MPEG-TS, which every DLNA renderer we target decodes. This is
// the one assumption we keep, and only as a floor.
func fallbackCaps() media.Renderer {
	return media.Renderer{
		SelfFetch:       dlna{}.selfFetches(),
		Containers:      []string{media.MPEGTS},
		Video:           []media.VideoSupport{videoSupportFor(media.CodecH264)},
		ServedContainer: servedContainer,
	}
}

// servedContainer is what castor muxes for a DLNA renderer. MPEG-TS is the one
// container this family is uniformly safe with: it is elementary-stream framed,
// so it can be produced as a single growing resource with no header to rewrite,
// which is what SetAVTransportURI plus a stream of unknown length needs. It is
// declared here, with the family that plays it, because it is a fact about the
// renderer and not about the pipeline: nothing in core or the executor may bake
// in a container of its own (see media.Renderer.ServedContainer).
const servedContainer = media.MPEGTS

// parseSinkProtocolInfo maps a ConnectionManager Sink protocolInfo CSV into a
// Renderer. Each entry is "protocol:network:mime:additionalInfo"; the codec is
// read from the DLNA.ORG_PN token (AVC/HEVC) and the MIME, and paired with its
// copy envelope. It is deliberately lenient: the lists renderers return are huge
// and vendor-specific, so anything unrecognised is skipped rather than rejected.
func parseSinkProtocolInfo(sink string) media.Renderer {
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

	// A DLNA renderer only plays a resource we push to it via SetAVTransportURI;
	// it never fetches an arbitrary source URL on its own, so SelfFetch stays
	// false (derived from dlna.selfFetches(), the single source of truth for
	// this family's static self-fetch bit) and the planner always serves the
	// stream from castor.
	r := media.Renderer{SelfFetch: dlna{}.selfFetches(), ServedContainer: servedContainer}
	for _, c := range discoverableCodecs {
		if present[c] {
			r.Video = append(r.Video, videoSupportFor(c))
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

// codecFromProfile identifies the video codec of a Sink entry from its DLNA.ORG_PN
// token (already upper-cased) or, failing that, its MIME type.
func codecFromProfile(mime, pn string) (media.Codec, bool) {
	switch {
	case strings.Contains(pn, "HEVC") || strings.Contains(pn, "H265") || strings.Contains(mime, "hevc") || strings.Contains(mime, "h265"):
		return media.CodecHEVC, true
	case strings.Contains(pn, "AVC") || strings.Contains(pn, "H264") || strings.Contains(mime, "avc") || strings.Contains(mime, "h264"):
		return media.CodecH264, true
	}
	return "", false
}

// audioFromProfile identifies an audio codec a Sink entry advertises, from its
// audio MIME type or the audio token in a combined AV DLNA.ORG_PN (already
// upper-cased) such as AVC_TS_HD_50_AC3_ISO or AVC_MP4_MP_HD_AAC. E-AC-3 is
// checked before AC-3 because its tokens ("EAC3", "DD+") contain the AC-3 ones.
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

// containerFromMIME normalises the DLNA MIME spellings we care about to the
// content types the pipeline speaks.
func containerFromMIME(mime string) (string, bool) {
	switch mime {
	case media.MPEGTS, "video/mpeg", "video/vnd.dlna.mpeg-tts", "video/x-mpegts":
		return media.MPEGTS, true
	case media.MP4:
		return media.MP4, true
	}
	return "", false
}

// codecNames renders a video-support set as its codec names, for logging.
func codecNames(vs []media.VideoSupport) []string {
	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = string(v.Codec)
	}
	return names
}

// transportLockedRetries and transportLockedDelay bound how long an AVTransport
// action waits out UPnP error 705 ("Transport is locked"). Renderers (Samsung
// TVs in particular) react to SetAVTransportURI by probing the resource
// themselves (a HEAD then a short GET; see the dance replay/server.go's
// handler expects) before their transport accepts the next action, so either
// SetAVTransportURI (racing a still-tearing-down prior session) or the Play
// that immediately follows it can land mid-probe and be rejected as locked.
// The fault clears once the probe/teardown finishes, so it is worth a short,
// bounded wait rather than failing the cast outright.
const (
	transportLockedRetries = 5
	transportLockedDelay   = 300 * time.Millisecond
)

// Play sets the AV transport URI and tells the renderer to begin playback.
// The pipeline burns subtitles directly into the video upstream of this call,
// so the renderer plays a single video resource with no separate caption track.
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

// retryTransportLocked calls do up to retries times, retrying while it fails
// with UPnP error 705 ("Transport is locked") and waiting delay between
// attempts. It stops early, returning the last error, if ctx is done first.
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

// isTransportLocked reports whether err is a UPnP fault carrying error code
// 705 ("Transport is locked").
func isTransportLocked(err error) bool {
	var fault *soap.SOAPFaultError
	return errors.As(err, &fault) && fault.Detail.UPnPError.Errorcode == 705
}

// action performs a SOAP action against the renderer's AVTransport service,
// namespaced to the service version the device actually published. None of the
// actions castor calls return out arguments, so no response is unmarshalled.
func (d *dlnaDevice) action(ctx context.Context, name string, request any) error {
	return d.transport.SOAPClient.PerformActionCtx(
		ctx, d.transport.Service.ServiceType, name, request, nil)
}

func (d *dlnaDevice) Close() error {
	return nil
}

// StreamHeaders returns the HTTP headers a DLNA renderer expects on a stream
// response. No Content-Length is set: the stream length is unknown and Samsung
// firmwares have been observed to mis-parse very large 64-bit values.
func (d *dlnaDevice) StreamHeaders(contentType string) map[string]string {
	return map[string]string{
		"Connection":               "close",
		"Accept-Ranges":            "none",
		"transferMode.dlna.org":    "Streaming",
		"contentFeatures.dlna.org": contentFeatures(contentType),
	}
}

// DLNA.ORG_FLAGS values (see DLNA Guidelines, Vol. 1, Table 4-129).
// flagsLive: SENDER_PACED+S0_INCREASE+SN_INCREASE+STREAMING+HTTP_STALLING+DLNA_V15.
// flagsFile: STREAMING+HTTP_STALLING+DLNA_V15.
const (
	dlnaFlagsLive = "8D300000000000000000000000000000"
	dlnaFlagsFile = "01300000000000000000000000000000"
)

// dlnaProfileFor returns the DLNA PN and FLAGS for a content type.
// MPEG_TS_HD_NA_ISO is for ffmpeg's 188-byte TS; the bare MPEG_TS_HD_NA
// profile is for 192-byte timestamped packets and Samsung rejects the mismatch.
func dlnaProfileFor(contentType string) (name, flags string) {
	switch contentType {
	case media.MPEGTS:
		return "MPEG_TS_HD_NA_ISO", dlnaFlagsLive
	case "video/mp4":
		return "AVC_MP4_HP_HD_AAC", dlnaFlagsFile
	}
	return "", dlnaFlagsLive
}

// contentFeatures returns a DLNA content features string for use in HTTP
// headers and DIDL metadata.
func contentFeatures(contentType string) string {
	name, flags := dlnaProfileFor(contentType)
	return fmt.Sprintf("DLNA.ORG_PN=%s;DLNA.ORG_OP=00;DLNA.ORG_CI=1;DLNA.ORG_FLAGS=%s", name, flags)
}
