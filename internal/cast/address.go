package cast

import (
	"context"
	"fmt"
	"net"
)

// LANAddress resolves castor's LAN address: the pinned interface's, or the default route's when none is pinned.
type LANAddress struct{ Interface string }

// LocalIPv4 dials a UDP socket to read the default-route address, or lists the pinned interface's addresses.
func (l LANAddress) LocalIPv4(ctx context.Context) (string, error) {
	if l.Interface == "" {
		conn, err := (&net.Dialer{}).DialContext(ctx, "udp4", "8.8.8.8:53")
		if err != nil {
			return "", fmt.Errorf("detecting default-route address (set network.interface to pin one): %w", err)
		}
		defer conn.Close()
		return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
	}

	iface, err := net.InterfaceByName(l.Interface)
	if err != nil {
		return "", fmt.Errorf("looking up interface %q: %w", l.Interface, err)
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

// Listen opens a delivery on the LAN address, where renderers on the network reach it.
func (l LANAddress) Listen(ctx context.Context) (net.Listener, error) {
	ip, err := l.LocalIPv4(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving local relay address: %w", err)
	}
	return net.Listen("tcp", net.JoinHostPort(ip, "0"))
}
