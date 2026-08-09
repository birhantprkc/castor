package core

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

// ResolveSource runs the do-or-die prelude that doesn't depend on the renderer:
// resolve the source URL (HLS rendition selection) and find our local IPv4. The
// device is discovered separately so its latency can overlap the puller.
//
// Three facts travel out beside the local address because they are three different
// kinds of thing and a cast needs all of them. The stream is what castor will read. The
// media.Origin is what the source published (the rendition ladder, how its segments are
// framed, whether it ends, how long it runs), all of it about a document only this phase
// reads, so carrying it forward is what lets a later judgement be made against what the
// source offered rather than against what castor happened to pick. The media.Rendition
// is which rung of that ladder was picked, which nothing else can reconstruct once the
// narrowing has rewritten the URL, and which is what a recovery measures a lighter rung
// against.
func ResolveSource(ctx context.Context, cfg Config, stream *media.Stream) (*media.Stream, media.Origin, media.Rendition, string, error) {
	slog.InfoContext(ctx, "resolving stream", "url", stream.URL.String())
	resolved, origin, chosen, err := cfg.Source.Resolve(ctx, stream, HandoffPossible(cfg))
	if err != nil {
		return nil, media.Origin{}, media.Rendition{}, "", fmt.Errorf("resolving URL: %w", err)
	}
	slog.InfoContext(ctx, "stream resolved", "url", resolved.URL.String(), "content_type", resolved.ContentType)

	localIP, err := localIPv4(cfg.Network.Interface)
	if err != nil {
		return nil, media.Origin{}, media.Rendition{}, "", fmt.Errorf("resolving local IP: %w", err)
	}
	return resolved, origin, chosen, localIP, nil
}

// HandoffPossible reports whether any composition of this cast could hand the renderer the
// source URL instead of serving it the bytes. It is the two facts that settle the question
// before a source has been looked at: what the family can do, read off the static profile
// (device.Profile), and what the operator asked for. Everything else Shape.Passthrough
// weighs is a property of the source, and this is here so that the parties who establish
// those properties can stop paying for them when nobody will read the answer.
//
// The profile is capability data, so no family is named here or anywhere below: the same
// expression answers for a family added tomorrow. It agrees with what a connected renderer
// reports (see device.Profile), which is what lets it be asked before one has been
// acquired, which is the only moment it is useful.
func HandoffPossible(cfg Config) bool {
	return device.Profile(cfg.Device.Type).SelfFetch && cfg.Delivery != media.DeliveryServe
}

// Connect locates and connects the renderer named in cfg. WHEN it is called is the
// executor's composition to decide and not this function's: a cast whose shape is already
// fixed by the family's static profile starts reading first and connects alongside, so slow
// discovery does not age a short-lived signed URL, while a cast whose shape depends on what
// the renderer negotiates has to connect before it can be composed at all.
//
// device.Connect dispatches on device type internally, which is why nothing above here
// carries a device-type switch.
func Connect(ctx context.Context, cfg Config) (device.Device, error) {
	info, err := resolveInfo(ctx, cfg)
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "device found", "name", info.Name, "type", string(info.Type), "address", info.Address)

	dev, err := device.Connect(ctx, info, cfg.Device)
	if err != nil {
		return nil, fmt.Errorf("connecting to device: %w", err)
	}
	slog.InfoContext(ctx, "connected to device", "name", info.Name)
	return dev, nil
}

// resolveInfo turns the configured target into a connectable Info. A pinned
// address (cfg.Device.Address) is honoured directly and skips discovery: the
// renderer is reached by unicast, so a cast works where SSDP/mDNS multicast does
// not (routed segments, or Android/Termux where interface enumeration is denied)
// and pays no discovery window. Absent an address it falls back to discovery,
// matching the device by name and type within the network timeout.
func resolveInfo(ctx context.Context, cfg Config) (device.Info, error) {
	if cfg.Device.Address != "" {
		slog.InfoContext(ctx, "pinning device by address", "name", cfg.Device.Name, "type", string(cfg.Device.Type), "address", cfg.Device.Address)
		ctx, cancel := context.WithTimeout(ctx, cfg.Network.Timeout)
		defer cancel()
		info, err := device.Locate(ctx, cfg.Device.Type, cfg.Device.Name, cfg.Device.Address)
		if err != nil {
			return device.Info{}, fmt.Errorf("pinning device: %w", err)
		}
		return info, nil
	}

	slog.InfoContext(ctx, "discovering device", "name", cfg.Device.Name, "type", string(cfg.Device.Type))
	info, err := device.FindInfo(ctx, cfg.Network.Timeout, cfg.Device.Type, cfg.Device.Name)
	if err != nil {
		return device.Info{}, fmt.Errorf("finding device: %w", err)
	}
	return info, nil
}

// localIPv4 returns the IPv4 address the local stream server should bind:
// the named interface's address, or, when name is empty, the source address
// of the default route. The UDP "connect" performs route selection only, no
// packet is sent.
func localIPv4(ifaceName string) (string, error) {
	if ifaceName == "" {
		conn, err := net.Dial("udp4", "8.8.8.8:53")
		if err != nil {
			return "", fmt.Errorf("detecting default-route address (set network.interface to pin one): %w", err)
		}
		defer conn.Close()
		return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
	}

	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return "", fmt.Errorf("looking up interface %q: %w", ifaceName, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return "", fmt.Errorf("listing addresses on %s: %w", iface.Name, err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if ip := ipNet.IP.To4(); ip != nil && !ip.IsLoopback() {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no IPv4 address on %s", iface.Name)
}
