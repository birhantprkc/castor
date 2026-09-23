// Package netaddr finds the local IPv4 address a renderer on the LAN reaches castor at.
package netaddr

import (
	"context"
	"fmt"
	"net"
)

// Local resolves castor's LAN address: the pinned interface's, or the default route's when none is pinned.
type Local struct{ Interface string }

// LocalIPv4 dials a UDP socket to read the default-route address, or lists the pinned interface's addresses.
func (l Local) LocalIPv4(ctx context.Context) (string, error) {
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
